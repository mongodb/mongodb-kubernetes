"""E2E for the search connectivity tool against a MultiCluster sharded source.x

Quarantined: flaky, producing unreliable results in CI. Tracking ticket: KUBE-161.
See EVERGREEN.md#quarantined-tests.
"""

from __future__ import annotations

from typing import List

import kubernetes
import pytest
from kubetester.mongodb_search import MongoDBSearch
from kubetester.multicluster_client import MultiClusterClient
from kubetester.phase import Phase
from tests import test_logger
from tests.common.search import search_resource_names
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
    cluster_tagged_read_preference,
    wait_for_mongot_statefulset_drained,
)
from tests.common.search.sharded_search_helper import mc_sharded_search_tester
from tests.conftest import get_member_cluster_clients, get_namespace

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

    def test_cluster_tagged_search_routes_in_cluster(
        self,
        namespace: str,
        central_cluster_client: kubernetes.client.ApiClient,
        member_cluster_clients: List[MultiClusterClient],
    ):
        """A ``nearest`` read preference tagged with a member cluster pins every shard's
        ``$search`` to a same-cluster member — so search traffic stays in that cluster and
        never crosses to another.

        mongos chooses the per-shard ``$search`` node via the client read preference
        (``findHosts``); ``primary`` mode ignores tagSets, so we use ``nearest`` + a
        ``nodeLocation:<clusterName>`` tagSet (members are tagged at deploy time via
        ``memberConfig[].tags``). mongod reaches mongot only through a fixed same-cluster
        ``mongotHost``, and the managed LB does not fan out across clusters — so the
        targeted member's cluster fully determines which mongot serves the query.

        Proof = a contrast matrix. With ONE cluster's per-shard mongots scaled to 0, the
        same query through the HEALTHY cluster's mongos:
          * tagged to the DOWNED cluster -> fails (``no healthy upstream`` /
            ``transient_network``): reads are pinned there and there is no cross-cluster
            mongot fallback;
          * tagged to the HEALTHY cluster -> succeeds.
        Same fault, opposite outcome decided purely by the tag => cluster-local routing.
        Queries always go through the healthy cluster's mongos so its planShardedSearch
        metadata mongot stays up and only the per-shard read-preference tag varies.
        """
        assert len(member_cluster_clients) >= 2, "locality contrast needs >= 2 member clusters"

        mdbs = _load_mdbs(namespace, central_cluster_client)
        original_clusters = [dict(c) for c in mdbs["spec"]["clusters"]]

        for faulted_index, faulted_mcc in enumerate(member_cluster_clients):
            healthy_index = next(i for i in range(len(member_cluster_clients)) if i != faulted_index)
            healthy_mcc = member_cluster_clients[healthy_index]
            faulted_name = faulted_mcc.cluster_name
            healthy_name = healthy_mcc.cluster_name
            tool = _user_connectivity_tool(namespace, healthy_index)
            faulted_stses = [
                search_resource_names.shard_statefulset_name(
                    MDBS_NAME, f"{MDB.mdb_resource_name}-{shard_idx}", faulted_index
                )
                for shard_idx in range(MDB.shard_count)
            ]

            mdbs.load()
            clusters = [dict(c) for c in original_clusters]
            for entry in clusters:
                if entry["name"] == faulted_name:
                    entry["replicas"] = 0
            mdbs["spec"]["clusters"] = clusters
            mdbs.update()
            logger.info(f"scaled cluster {faulted_index} ({faulted_name}) per-shard mongots -> 0")
            try:
                for sts_name in faulted_stses:
                    wait_for_mongot_statefulset_drained(
                        sts_name, namespace, api_client=faulted_mcc.api_client, timeout=300
                    )

                healthy_res = tool.oneshot_search(read_preference=cluster_tagged_read_preference(healthy_name))
                logger.info(f"[{faulted_name} down] tag->{healthy_name} (healthy): {healthy_res}")
                assert healthy_res.success, (
                    f"tag->{healthy_name} must serve while {faulted_name} mongots are down: "
                    f"{healthy_res.error_class} {healthy_res.error_message}"
                )
                assert healthy_res.returned_count > 0, f"tag->{healthy_name} returned 0 docs"

                faulted_res = tool.oneshot_search(read_preference=cluster_tagged_read_preference(faulted_name))
                logger.info(f"[{faulted_name} down] tag->{faulted_name} (faulted): {faulted_res}")
                assert not faulted_res.success, (
                    f"tag->{faulted_name} unexpectedly served with that cluster's mongots down "
                    f"(would imply cross-cluster routing): {faulted_res}"
                )
                assert faulted_res.failure_class == FAILURE_TRANSIENT_NETWORK, (
                    f"tag->{faulted_name}: expected transient_network (no healthy upstream), got "
                    f"{faulted_res.failure_class} ({faulted_res.error_class}): {faulted_res.error_message}"
                )
            finally:
                mdbs.load()
                mdbs["spec"]["clusters"] = [dict(c) for c in original_clusters]
                mdbs.update()
                mdbs.assert_reaches_phase(Phase.Running, timeout=1800)
                # re-index the restored cluster's mongots before the next iteration
                tool.wait_for_sentinel_indexed(timeout=300)
                logger.info(f"restored cluster {faulted_index} ({faulted_name}) mongots; MDBS Running")


def _user_connectivity_tool(namespace: str, cluster_index: int) -> SearchConnectivityTool:
    return SearchConnectivityTool(
        mc_sharded_search_tester(MDBS_NAME, namespace, cluster_index, MDB.user_name, MDB.user_password)
    )


def _load_mdbs(namespace: str, central_cluster_client: kubernetes.client.ApiClient) -> MongoDBSearch:
    resource = MongoDBSearch(name=MDBS_NAME, namespace=namespace)
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    resource.load()
    return resource
