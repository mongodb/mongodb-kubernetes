"""E2E for the search connectivity tool against a MultiCluster sharded source.x"""

from __future__ import annotations

from typing import List

import kubernetes
import pytest
from kubetester.mongodb_search import MongoDBSearch
from kubetester.multicluster_client import MultiClusterClient
from kubetester.phase import Phase
from tests import test_logger
from tests.common.search import search_resource_names
from tests.common.search.background_availability_tester import MultiClusterAvailabilityFleet
from tests.common.search.bootstrap_test_mixins import (
    InstallOperatorTests,
    MongoDBDeploymentConfig,
    SampleDataAndIndexConfig,
    SearchDeploymentConfig,
    SearchShardedSampleDataAndIndex,
)
from tests.common.search.bootstrap_test_mixins_mc import (
    MongoDBMultiShardedDeploymentTests,
    SearchShardedMcDeploymentTests,
)
from tests.common.search.connectivity import (
    FAILURE_TRANSIENT_NETWORK,
    SearchConnectivityTool,
    scale_mongot_statefulset,
    set_resource_disabled_annotation,
    wait_for_mongot_statefulset_drained,
)
from tests.common.search.sharded_search_helper import mc_sharded_search_tester
from tests.conftest import get_central_cluster_client, get_member_cluster_clients, get_namespace

logger = test_logger.get_test_logger(__name__)

pytestmark = pytest.mark.e2e_search_connectivity_tool_mc_sharded

NAMESPACE = get_namespace()
MDB = MongoDBDeploymentConfig(mdb_resource_name="mdb-mc-sh-conn-tool", shard_count=2)
SEARCH = SearchDeploymentConfig(mongot_replicas=1, create_timeout=900)
MDBS_NAME = MDB.mdb_resource_name


class TestInstallOperator(InstallOperatorTests):
    pass


class TestMongoDBShardedDeployment(MongoDBMultiShardedDeploymentTests):
    namespace = NAMESPACE
    mdb_config = MDB
    search_config = SEARCH


class TestSearchShardedDeployment(SearchShardedMcDeploymentTests):
    namespace = NAMESPACE
    mdb_config = MDB
    search_config = SEARCH


class TestSampleData(SearchShardedSampleDataAndIndex):
    mdb_config = MDB
    # ~70k corpus (≈21k restored + 50k synthetic) so movies spread across both shards and
    # paging exercises real cross-shard getMore round-trips.
    sample_config = SampleDataAndIndexConfig(extra_doc_count=150_000)

    def tools_pod_api_client(self) -> kubernetes.client.ApiClient:
        # mongorestore runs from a member cluster so it can resolve the source mongos.
        return get_member_cluster_clients()[0].api_client

    def admin_tester(self, namespace: str):
        return mc_sharded_search_tester(MDBS_NAME, namespace, 0, MDB.admin_user_name, MDB.admin_user_password)

    def user_tester(self, namespace: str):
        return mc_sharded_search_tester(MDBS_NAME, namespace, 0, MDB.user_name, MDB.user_password)


# ---------------------------------------------------------------------------
# Connectivity-tool tests — through each member cluster's mongos.
# ---------------------------------------------------------------------------


class TestSearchConnectivityToolMcSharded:
    """SearchConnectivityTool against the MC sharded source, through per-cluster mongos."""

    def test_oneshot_search_through_cluster0_mongos(self, namespace: str):
        result = _user_connectivity_tool(namespace, 0).oneshot_search()
        logger.info(f"oneshot_search result: {result}")
        assert result.success, f"one-shot search failed: {result.error_class} {result.error_message}"
        assert result.returned_count > 0, "expected results from cache-busted compound query"

    def test_paging_search_first_page_succeeds(self, namespace: str):
        pages = _user_connectivity_tool(namespace, 0).paging_search(pages=3, interval_seconds=0.1, batch_size=20)
        logger.info("paging_search results: %s", "; ".join(str(p) for p in pages))
        assert pages, "paging_search returned no pages"
        assert pages[0].success, f"first page failed: {pages[0].error_class} {pages[0].error_message}"
        assert pages[0].returned_count > 0, "first page returned 0 docs"

    def test_per_cluster_mongos_search_returns_results(
        self,
        namespace: str,
        member_cluster_clients: List[MultiClusterClient],
    ):
        """$search via each cluster's mongos returns docs — proves every cluster's
        Envoy + per-shard mongot fan-out path serves the cross-shard query."""
        for cluster_index in range(len(member_cluster_clients)):
            result = _user_connectivity_tool(namespace, cluster_index).oneshot_search()
            logger.info(f"cluster {cluster_index}: oneshot result: {result}")
            assert (
                result.success
            ), f"cluster {cluster_index}: $search via mongos failed: {result.error_class} {result.error_message}"
            assert result.returned_count > 0, f"cluster {cluster_index}: $search returned 0 docs"

    def test_data_balanced_across_shards(self, namespace: str):
        """sample_mflix.movies distributed across both shards; each shard holds ≥ 50 docs."""
        tester = mc_sharded_search_tester(MDBS_NAME, namespace, 0, MDB.user_name, MDB.user_password)
        coll = tester.client["sample_mflix"]["movies"]
        stats = list(coll.aggregate([{"$collStats": {"storageStats": {}}}]))
        per_shard = {s.get("shard") or s.get("host"): s.get("storageStats", {}).get("count", 0) for s in stats}
        total = sum(per_shard.values())
        logger.info(f"per-shard movies counts: {per_shard} total={total}")
        assert (
            len(per_shard) >= MDB.shard_count
        ), f"expected $collStats from {MDB.shard_count} shards; got {len(per_shard)}: {per_shard}"
        for shard, count in per_shard.items():
            assert count >= 50, f"shard {shard} has only {count} docs"

    # No envoy-restart fault for sharded: the mongos→envoy→mongot cursor rides through a
    # brief envoy restart (auto-replaced in seconds). The reliable single-cluster fault is
    # a sustained mongot outage — the dual-mode fleet test below.

    def test_oneshot_search_surfaces_fanout_error_when_one_shard_mongot_down(
        self,
        namespace: str,
        member_cluster_clients: List[MultiClusterClient],
    ):
        """A NEW ``$search`` establishes a mongot cursor on EVERY shard, so taking ONE
        shard's mongot offline in ONE cluster makes that shard's ``planShardedSearch``
        fan-out fail — and a oneshot query surfaces the connectivity error through every
        cluster's mongos.

        Note this is the opposite of an *already-established* paging cursor, which
        survives mongot loss: each shard eagerly drains its whole mongot result into a
        local buffer in one getMore at establishment, so client paging is served locally
        and never needs mongot again. Only a fresh query (oneshot) re-contacts mongot and
        therefore exposes a downed shard.
        """
        outage_index = 0  # cluster_index in member_cluster_clients enumeration order
        outage_mcc = member_cluster_clients[outage_index]
        # shard 0's mongot StatefulSet in the faulted cluster
        shard_sts_name = search_resource_names.shard_statefulset_name(
            MDBS_NAME, f"{MDB.mdb_resource_name}-0", outage_index
        )

        # precondition: oneshot serves before the fault
        pre = _user_connectivity_tool(namespace, 0).oneshot_search()
        assert pre.success, f"precondition: oneshot must succeed before the fault: {pre}"

        # Pause the operator so it won't reconcile the single-STS replica change back, then
        # take shard 0's mongot offline in just this one cluster.
        mdbs = _load_mdbs(namespace, get_central_cluster_client())
        set_resource_disabled_annotation(mdbs, True)
        try:
            scale_mongot_statefulset(shard_sts_name, namespace, 0, api_client=outage_mcc.api_client)
            wait_for_mongot_statefulset_drained(
                shard_sts_name, namespace, api_client=outage_mcc.api_client, timeout=300
            )

            # A fresh oneshot $search must fan out to shard 0 (mongot gone) -> connectivity
            # error, regardless of which cluster's mongos serves the query.
            for cluster_index in range(len(member_cluster_clients)):
                result = _user_connectivity_tool(namespace, cluster_index).oneshot_search()
                logger.info(f"cluster {cluster_index} oneshot during shard-0 mongot outage: {result}")
                assert (
                    not result.success
                ), f"cluster {cluster_index}: oneshot unexpectedly succeeded with shard-0 mongot down: {result}"
                assert result.failure_class == FAILURE_TRANSIENT_NETWORK, (
                    f"cluster {cluster_index}: expected a transient_network fan-out error, got "
                    f"{result.failure_class} ({result.error_class}): {result.error_message}"
                )
        finally:
            scale_mongot_statefulset(shard_sts_name, namespace, 1, api_client=outage_mcc.api_client)
            mdbs = _load_mdbs(namespace, get_central_cluster_client())
            set_resource_disabled_annotation(mdbs, False)
            # confirm the shard's mongot re-indexed and search serves again before moving on
            _user_connectivity_tool(namespace, 0).wait_for_sentinel_indexed(timeout=300)
            logger.info("restored shard-0 mongot; search serving again")

    def test_single_cluster_mongot_outage_preserves_other_clusters(
        self,
        namespace: str,
        central_cluster_client: kubernetes.client.ApiClient,
        member_cluster_clients: List[MultiClusterClient],
    ):
        """Scale ONE cluster's mongot to 0 (draining all its per-shard mongots) while a
        dual-mode (oneshot + paging) fleet runs per cluster's mongos: the faulted cluster
        surfaces an outage in both modes, the others serve continuously."""
        mdbs = _load_mdbs(namespace, central_cluster_client)
        original_clusters = [dict(c) for c in mdbs["spec"]["clusters"]]

        outage_index = 0  # cluster_index == member_cluster_clients enumeration order
        outage_mcc = member_cluster_clients[outage_index]
        indexes = list(range(len(member_cluster_clients)))
        shard_stses = [
            search_resource_names.shard_statefulset_name(
                MDBS_NAME, f"{MDB.mdb_resource_name}-{shard_idx}", outage_index
            )
            for shard_idx in range(MDB.shard_count)
        ]

        with MultiClusterAvailabilityFleet(
            tester_factory=lambda idx: _user_connectivity_tool(namespace, idx),
            cluster_indexes=indexes,
            interval_seconds=0.2,
        ) as fleet:
            fleet.wait_for_operations_all(5)
            try:
                mdbs.load()
                clusters = [dict(c) for c in original_clusters]
                for entry in clusters:
                    if entry["clusterName"] == outage_mcc.cluster_name:
                        entry["replicas"] = 0
                mdbs["spec"]["clusters"] = clusters
                mdbs.update()
                logger.info(f"scaled cluster {outage_index} ({outage_mcc.cluster_name}) per-shard mongot -> 0")
                for sts_name in shard_stses:
                    wait_for_mongot_statefulset_drained(
                        sts_name, namespace, api_client=outage_mcc.api_client, timeout=300
                    )
                fleet.wait_for_operations_all(40)  # observe through the outage
            finally:
                mdbs.load()
                mdbs["spec"]["clusters"] = [dict(c) for c in original_clusters]
                mdbs.update()
                mdbs.assert_reaches_phase(Phase.Running, timeout=600)
                logger.info(f"restored cluster {outage_index} mongot replicas, MDBS back to Running")

        fleet.assert_single_cluster_outage(outage_index)


def _user_connectivity_tool(namespace: str, cluster_index: int) -> SearchConnectivityTool:
    return SearchConnectivityTool(
        mc_sharded_search_tester(MDBS_NAME, namespace, cluster_index, MDB.user_name, MDB.user_password)
    )


def _load_mdbs(namespace: str, central_cluster_client: kubernetes.client.ApiClient) -> MongoDBSearch:
    resource = MongoDBSearch(name=MDBS_NAME, namespace=namespace)
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    resource.load()
    return resource
