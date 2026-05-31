"""E2E tests for the search connectivity tool — sharded variant.
"""

from __future__ import annotations

import pytest
from kubetester import list_matching_pods, wait_for_no_pods_ready, wait_for_pods_ready
from kubetester.kubetester import run_periodically
from kubetester.mongodb import MongoDB
from kubetester.mongodb_search import MongoDBSearch
from kubetester.phase import Phase
from tests import test_logger
from tests.common.search import search_resource_names
from tests.common.search.bootstrap_test_mixins import (
    MongoDBShardedDeploymentConfig,
    MongoDBShardedDeploymentTests,
    SearchShardedDeploymentTests,
    SearchShardedE2EFixtures,
    SearchShardedSampleDataAndIndex,
    InstallOperatorTests,
)
from tests.common.search.connectivity import (
    SearchConnectivityTool,
    delete_pods,
    patch_mongot_readiness_probe_to_false,
    restore_mongot_readiness_probe,
    set_resource_disabled_annotation,
)
from tests.common.search.sharded_search_helper import get_search_tester

logger = test_logger.get_test_logger(__name__)

pytestmark = pytest.mark.e2e_search_connectivity_tool_sharded


def build_conn_tool_sharded_config() -> MongoDBShardedDeploymentConfig:
    # User names auto-derive from mdb_resource_name in __post_init__.
    return MongoDBShardedDeploymentConfig(mdb_resource_name="mdb-sh-conn-tool")


class TestInstallOperator(InstallOperatorTests):
    pass


class TestSearchWithShardedCluster(
    SearchShardedDeploymentTests,
    MongoDBShardedDeploymentTests,
):
    def build_mongodb_sharded_config(self) -> MongoDBShardedDeploymentConfig:
        return build_conn_tool_sharded_config()


class TestSearchSampleDataAndIndex(
    SearchShardedSampleDataAndIndex,  # Layer 3 sharded — overrides _post_restore_setup
    SearchShardedE2EFixtures,
):
    def build_mongodb_sharded_config(self) -> MongoDBShardedDeploymentConfig:
        return build_conn_tool_sharded_config()


class TestSearchConnectivityToolSharded(
    SearchShardedE2EFixtures,
):
    def build_mongodb_sharded_config(self) -> MongoDBShardedDeploymentConfig:
        return build_conn_tool_sharded_config()

    def test_data_balanced_across_shards(self, mdb: MongoDB):
        """sample_mflix.movies distributed across both shards.

        Total corpus is ~21k restored docs + ``extra_doc_count`` synthetic
        ones — expect ≥ ``total/shard_count * 0.4`` per shard (allow 40/60
        skew); both shards each must hold ≥ 50 docs.
        """
        cfg = self.build_mongodb_sharded_config()
        sample_cfg = self.build_sample_data_and_index_config()
        search_tester = get_search_tester(mdb, cfg.user_name, cfg.user_password, use_ssl=True)
        coll = search_tester.client["sample_mflix"]["movies"]
        stats = list(coll.aggregate([{"$collStats": {"storageStats": {}}}]))
        per_shard = {s.get("shard") or s.get("host"): s.get("storageStats", {}).get("count", 0) for s in stats}
        total = sum(per_shard.values())
        synthetic = coll.count_documents({"synthetic": True})
        logger.info(f"per-shard movies counts: {per_shard} total={total} synthetic={synthetic}")
        assert (
            len(per_shard) >= cfg.shard_count
        ), f"expected $collStats from {cfg.shard_count} shards; got {len(per_shard)}: {per_shard}"
        for shard, count in per_shard.items():
            assert count >= 50, f"shard {shard} has only {count} docs"
        if sample_cfg.extra_doc_count > 0:
            assert (
                synthetic >= sample_cfg.extra_doc_count
            ), f"only {synthetic} synthetic docs present; expected ≥ {sample_cfg.extra_doc_count}"
            min_expected = int(total / cfg.shard_count * 0.6)
            for shard, count in per_shard.items():
                assert (
                    count >= min_expected
                ), f"shard {shard} has {count} docs; expected ≥ {min_expected} for balanced split"

    def test_query_fails_when_envoy_endpoints_removed_for_one_shard(
        self, mdb: MongoDB, mdbs: MongoDBSearch, namespace: str
    ):
        """Pause MongoDBSearch reconcile, break shard-0 mongot readiness, fanout $search fails.

        Uses the mongodb.com/resourceDisabled annotation (gated by the
        MongoDBSearch reconciler) so the operator stops rewriting the
        shard-0 mongot StatefulSet template. Then injects a /bin/false
        readiness probe on shard-0's mongot container and bounces those
        pods so the new ones come up NotReady. Envoy drops them from its
        per-shard endpoint set; the fanout $search to shard-0 has no
        upstream and the whole query fails.
        """
        cfg = self.build_mongodb_sharded_config()
        shard_name = f"{cfg.mdb_resource_name}-0"
        sts_name = search_resource_names.shard_statefulset_name(mdbs.name, shard_name)
        pod_prefix = sts_name + "-"

        search_tester = get_search_tester(mdb, cfg.user_name, cfg.user_password, use_ssl=True)
        tool = SearchConnectivityTool(search_tester)

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
            assert not result.success, (
                f"fanout query unexpectedly succeeded with shard-0 mongot NotReady; " f"result={result}"
            )
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

            # Envoy needs a few seconds to re-resolve the freshly-Ready
            # mongot endpoints after the probe is restored; poll instead
            # of asserting once.
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
