"""E2E tests for the search connectivity tool against a single-cluster sharded source."""

from __future__ import annotations

import pytest
from kubetester import list_matching_pods, wait_for_no_pods_ready, wait_for_pods_ready
from kubetester.kubetester import run_periodically
from kubetester.mongodb_search import MongoDBSearch
from kubetester.phase import Phase
from tests import test_logger
from tests.common.search import search_resource_names
from tests.common.search.bootstrap_test_mixins import (
    InstallOperatorTests,
    MongoDBDeploymentConfig,
    MongoDBShardedDeploymentTests,
    SampleDataAndIndexConfig,
    SearchDeploymentConfig,
    SearchShardedDeploymentTests,
    SearchShardedSampleDataAndIndex,
)
from tests.common.search.connectivity import (
    SearchConnectivityTool,
    delete_pods,
    patch_mongot_readiness_probe_to_false,
    restore_mongot_readiness_probe,
    set_resource_disabled_annotation,
)
from tests.common.search.sharded_search_helper import sharded_search_tester
from tests.conftest import get_namespace

logger = test_logger.get_test_logger(__name__)

pytestmark = pytest.mark.e2e_search_connectivity_tool_sharded

NAMESPACE = get_namespace()
MDB = MongoDBDeploymentConfig(mdb_resource_name="mdb-sh-conn-tool", shard_count=2)
SEARCH = SearchDeploymentConfig()
MDBS_NAME = MDB.mdb_resource_name


def _user_tool(namespace: str) -> SearchConnectivityTool:
    return SearchConnectivityTool(sharded_search_tester(MDBS_NAME, namespace, MDB.user_name, MDB.user_password))


def _load_mdbs(namespace: str) -> MongoDBSearch:
    resource = MongoDBSearch(name=MDBS_NAME, namespace=namespace)
    resource.load()
    return resource


class TestInstallOperator(InstallOperatorTests):
    pass


class TestMongoDBShardedDeployment(MongoDBShardedDeploymentTests):
    namespace = NAMESPACE
    mdb_config = MDB
    search_config = SEARCH


class TestSearchShardedDeployment(SearchShardedDeploymentTests):
    namespace = NAMESPACE
    mdb_config = MDB
    search_config = SEARCH


class TestSampleData(SearchShardedSampleDataAndIndex):
    mdb_config = MDB
    # ~70k corpus (≈21k restored + 50k synthetic) so cross-shard fanout paging
    # has enough pages for cursor-loss tests.
    sample_config = SampleDataAndIndexConfig(extra_doc_count=50_000)

    def admin_tester(self, namespace: str):
        return sharded_search_tester(MDBS_NAME, namespace, MDB.admin_user_name, MDB.admin_user_password)

    def user_tester(self, namespace: str):
        return sharded_search_tester(MDBS_NAME, namespace, MDB.user_name, MDB.user_password)


class TestSearchConnectivityToolSharded:
    """SearchConnectivityTool against the sharded source, through mongos."""

    def test_oneshot_search_succeeds(self, namespace: str):
        result = _user_tool(namespace).oneshot_search()
        logger.info(f"oneshot_search result: {result}")
        assert result.success, f"one-shot search failed: {result.error_class} {result.error_message}"
        assert result.returned_count > 0, "expected results from cache-busted compound query"

    def test_paging_search_first_page_succeeds(self, namespace: str):
        pages = _user_tool(namespace).paging_search(pages=3, interval_seconds=0.1, batch_size=20)
        logger.info("paging_search results: %s", "; ".join(str(p) for p in pages))
        assert pages, "paging_search returned no pages"
        assert pages[0].success, f"first page failed: {pages[0].error_class} {pages[0].error_message}"
        assert pages[0].returned_count > 0, "first page returned 0 docs"

    def test_data_balanced_across_shards(self, namespace: str):
        """sample_mflix.movies distributed across both shards.

        Corpus is ~21k restored + 50k synthetic docs — expect a roughly even
        split (allow 40/60 skew); both shards must each hold ≥ 50 docs.
        """
        tester = sharded_search_tester(MDBS_NAME, namespace, MDB.user_name, MDB.user_password)
        coll = tester.client["sample_mflix"]["movies"]
        stats = list(coll.aggregate([{"$collStats": {"storageStats": {}}}]))
        per_shard = {s.get("shard") or s.get("host"): s.get("storageStats", {}).get("count", 0) for s in stats}
        total = sum(per_shard.values())
        synthetic = coll.count_documents({"synthetic": True})
        logger.info(f"per-shard movies counts: {per_shard} total={total} synthetic={synthetic}")
        assert (
            len(per_shard) >= MDB.shard_count
        ), f"expected $collStats from {MDB.shard_count} shards; got {len(per_shard)}: {per_shard}"
        for shard, count in per_shard.items():
            assert count >= 50, f"shard {shard} has only {count} docs"
        min_expected = int(total / MDB.shard_count * 0.6)
        for shard, count in per_shard.items():
            assert (
                count >= min_expected
            ), f"shard {shard} has {count} docs; expected ≥ {min_expected} for balanced split"

    def test_query_fails_when_envoy_endpoints_removed_for_one_shard(self, namespace: str):
        """Pause MongoDBSearch reconcile, break shard-0 mongot readiness, fanout $search fails.

        The disable-reconciliation annotation stops the operator rewriting the shard-0
        mongot StatefulSet; a /bin/false readiness probe brings its pods up
        NotReady. Envoy drops them from the shard-0 endpoint set so the fanout
        $search to shard-0 has no upstream and the whole query fails.
        """
        shard_name = f"{MDB.mdb_resource_name}-0"
        sts_name = search_resource_names.shard_statefulset_name(MDBS_NAME, shard_name)
        pod_prefix = sts_name + "-"

        tool = _user_tool(namespace)
        mdbs = _load_mdbs(namespace)

        pre = tool.oneshot_search()
        assert pre.success, f"pre-disrupt search failed: {pre.error_class} {pre.error_message}"

        original_pod_count = len(list_matching_pods(namespace, name_prefix=pod_prefix))
        assert original_pod_count > 0, f"no shard-0 mongot pods found with prefix {pod_prefix}"

        annotation_set = False
        probe_patched = False
        try:
            set_resource_disabled_annotation(mdbs, True)
            annotation_set = True

            patch_mongot_readiness_probe_to_false(namespace, sts_name)
            probe_patched = True
            delete_pods(namespace, name_prefix=pod_prefix, grace_period_seconds=0)
            wait_for_no_pods_ready(namespace, name_prefix=pod_prefix, timeout=180)

            result = tool.oneshot_search()
            logger.info(
                f"post-disrupt result: success={result.success} failure_class={result.failure_class} "
                f"error={result.error_class}({result.error_code}) msg={result.error_message}"
            )
            assert (
                not result.success
            ), f"fanout query unexpectedly succeeded with shard-0 mongot NotReady; result={result}"
            assert result.failure_class in {
                "transient_network",
                "other",
            }, f"unexpected failure_class={result.failure_class}; result={result}"
        finally:
            if probe_patched:
                restore_mongot_readiness_probe(namespace, sts_name)
            delete_pods(namespace, name_prefix=pod_prefix, grace_period_seconds=0)
            if annotation_set:
                set_resource_disabled_annotation(mdbs, False)
            mdbs.assert_reaches_phase(Phase.Running, timeout=300)
            wait_for_pods_ready(namespace, name_prefix=pod_prefix, expected_count=original_pod_count, timeout=300)

            # Envoy needs a few seconds to re-resolve the freshly-Ready mongot
            # endpoints after the probe is restored; poll instead of asserting once.
            def search_ok() -> tuple[bool, str]:
                r = tool.oneshot_search()
                if r.success:
                    return True, f"oneshot recovered (returned={r.returned_count})"
                return False, f"{r.error_class}: {r.error_message}"

            run_periodically(
                search_ok,
                timeout=120,
                sleep_time=3,
                msg="post-recovery $search to succeed (envoy endpoint refresh)",
            )
