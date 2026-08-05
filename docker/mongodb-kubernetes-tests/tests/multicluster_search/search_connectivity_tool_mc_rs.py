"""E2E for the search connectivity tool against a 3-cluster MongoDBMulti RS."""

from __future__ import annotations

from typing import List

import kubernetes
import pytest
from kubetester.mongodb_search import MongoDBSearch
from kubetester.multicluster_client import MultiClusterClient
from kubetester.phase import Phase
from tests import test_logger
from tests.common.search.background_availability_tester import MultiClusterAvailabilityFleet
from tests.common.search.bootstrap_test_mixins import (
    InstallOperatorTests,
    MongoDBDeploymentConfig,
    SampleDataAndIndexConfig,
    SearchDeploymentConfig,
    SearchSampleDataAndIndexTests,
)
from tests.common.search.bootstrap_test_mixins_mc import MongoDBMultiRsDeploymentTests, SearchRsMcDeploymentTests
from tests.common.search.connectivity import (
    SearchConnectivityTool,
    assert_disruption_observed,
    hard_kill_pods_by_label,
    paging_baseline_and_fault,
    wait_for_mongot_pvcs_deleted,
    wait_for_mongot_statefulset_drained,
    wait_for_pods_by_label_replaced,
)
from tests.common.search.rs_search_helper import mc_rs_search_tester, mc_rs_search_tester_for_cluster_member
from tests.common.search.search_deployment_helper import MCSearchDeploymentHelper
from tests.conftest import get_member_cluster_clients, get_namespace

logger = test_logger.get_test_logger(__name__)

pytestmark = pytest.mark.e2e_search_connectivity_tool_mc_rs

NAMESPACE = get_namespace()
MDB = MongoDBDeploymentConfig(mdb_resource_name="mdb-mc-rs-conn-tool")
SEARCH = SearchDeploymentConfig(mongot_replicas=1, create_timeout=900)
MDBS_NAME = MDB.mdb_resource_name


class TestInstallOperator(InstallOperatorTests):
    pass


class TestMongoDBDeployment(MongoDBMultiRsDeploymentTests):
    namespace = NAMESPACE
    mdb_config = MDB
    search_config = SEARCH


class TestSearchDeployment(SearchRsMcDeploymentTests):
    namespace = NAMESPACE
    mdb_config = MDB
    search_config = SEARCH


class TestSampleData(SearchSampleDataAndIndexTests):
    sample_config = SampleDataAndIndexConfig()

    def tools_pod_api_client(self) -> kubernetes.client.ApiClient:
        return get_member_cluster_clients()[0].api_client

    def admin_tester(self, namespace: str):
        return mc_rs_search_tester(MDBS_NAME, namespace, MDB.admin_user_name, MDB.admin_user_password)

    def user_tester(self, namespace: str):
        return mc_rs_search_tester(MDBS_NAME, namespace, MDB.user_name, MDB.user_password)


# ---------------------------------------------------------------------------
# Connectivity-tool tests.
# ---------------------------------------------------------------------------


class TestSearchConnectivityToolMC:
    """SearchConnectivityTool against the MC RS source.

    Primary-routed tests (oneshot / first-page) use a replicaSet= URI; the
    per-cluster fault tests use directConnection=true to pin a specific cluster
    and assert disruption locality while the other clusters keep serving.
    """

    # Happy path — through the RS primary.

    def test_oneshot_search_through_primary(self, namespace: str):
        result = _user_connectivity_tool(namespace).oneshot_search()
        logger.info(f"oneshot_search result: {result}")
        assert result.success, f"one-shot search failed: {result.error_class} {result.error_message}"
        assert result.returned_count > 0, "expected results from cache-busted compound query"

    def test_paging_search_first_page_succeeds(self, namespace: str):
        pages = _user_connectivity_tool(namespace).paging_search(pages=3, interval_seconds=0.1, batch_size=20)
        logger.info("paging_search: %s", "; ".join(str(p) for p in pages))
        assert pages, "paging_search returned no pages"
        assert pages[0].success, f"first page failed: {pages[0].error_class} {pages[0].error_message}"
        assert pages[0].returned_count > 0, "first page returned 0 docs"

    # Per-cluster fan-out — fault paths.

    def test_paging_through_per_cluster_mongot_outage(
        self,
        namespace: str,
        central_cluster_client: kubernetes.client.ApiClient,
        member_cluster_clients: List[MultiClusterClient],
    ):
        """For each cluster: scale its mongot to 0, confirm the direct-N tester surfaces
        disruption on a fresh cursor while the other clusters keep serving, then restore.

        Fault is applied via CRD patch (not mid-cursor) because operator reconcile
        latency exceeds the paging drain window.
        """
        helper = _mc_helper(namespace, member_cluster_clients)
        mdbs = _load_mdbs(namespace, central_cluster_client)
        original_clusters = [dict(c) for c in mdbs["spec"]["clusters"]]

        for outage_mcc in member_cluster_clients:
            outage_idx = helper.cluster_index(outage_mcc.cluster_name)
            outage_label = f"{outage_mcc.cluster_name}/idx={outage_idx}"
            sts_name = f"{MDBS_NAME}-search-{outage_idx}"
            logger.info(f"=== {outage_label}: starting mongot-outage scenario ===")

            tool = _direct_user_connectivity_tool(namespace, outage_idx)

            pre_pages = tool.paging_search(pages=2, interval_seconds=0.1, batch_size=10)
            logger.info(f"{outage_label}: pre-outage pages: %s", "; ".join(str(p) for p in pre_pages))
            assert any(p.success for p in pre_pages), f"{outage_label}: no pre-outage successful page"

            try:
                mdbs.load()
                clusters = [dict(c) for c in original_clusters]
                for entry in clusters:
                    if entry["name"] == outage_mcc.cluster_name:
                        entry["replicas"] = 0
                mdbs["spec"]["clusters"] = clusters
                mdbs.update()
                logger.info(f"{outage_label}: set spec.clusters replicas=0 for this cluster")

                # The mongot STS lives on the OUTAGE cluster, so query it there —
                # the default client points at the operator cluster and would 404.
                wait_for_mongot_statefulset_drained(
                    sts_name,
                    namespace,
                    api_client=outage_mcc.api_client,
                    timeout=300,
                )
                logger.info(f"{outage_label}: mongot STS {sts_name} has drained to 0 replicas")

                post_pages = tool.paging_search(pages=20, interval_seconds=0.5, batch_size=10)
                logger.info(f"{outage_label}: post-outage pages: %s", "; ".join(str(p) for p in post_pages))
                post_verdict = tool.verdict(post_pages)
                logger.info(f"{outage_label}: post-outage verdict: {post_verdict.as_dict()}")
                assert_disruption_observed(post_verdict, context=f"{outage_label}: cluster mongot outage")

                for other_mcc in member_cluster_clients:
                    if other_mcc.cluster_name == outage_mcc.cluster_name:
                        continue
                    other_idx = helper.cluster_index(other_mcc.cluster_name)
                    other_result = _direct_user_connectivity_tool(namespace, other_idx).oneshot_search()
                    assert other_result.success, (
                        f"cross-cluster survival: cluster {other_idx} should serve while "
                        f"{outage_label} is out, got {other_result}"
                    )
                    logger.info(
                        f"cross-cluster survival OK: {other_mcc.cluster_name}/idx={other_idx} "
                        f"served while {outage_label} mongot was scaled to 0"
                    )
            finally:
                # Wait out the async PVC GC before restoring replicas (see
                # wait_for_mongot_pvcs_deleted docstring for the STS-controller race).
                wait_for_mongot_pvcs_deleted(
                    namespace,
                    sts_name,
                    api_client=outage_mcc.api_client,
                    timeout=300,
                )
                mdbs.load()
                mdbs["spec"]["clusters"] = [dict(c) for c in original_clusters]
                mdbs.update()
                # The fresh per-cluster replica must reindex from mongod before it
                # reports Ready, so allow a generous window.
                mdbs.assert_reaches_phase(Phase.Running, timeout=1200)
                logger.info(f"{outage_label}: restored per-cluster replicas, MDBS back to Running")

    def test_paging_through_per_cluster_envoy_restart(
        self,
        namespace: str,
        member_cluster_clients: List[MultiClusterClient],
    ):
        """For each cluster: hard-kill its envoy pods, confirm the direct-N tester surfaces
        disruption while the other clusters keep serving, then wait for the replacements.
        """
        helper = _mc_helper(namespace, member_cluster_clients)
        for outage_mcc in member_cluster_clients:
            outage_idx = helper.cluster_index(outage_mcc.cluster_name)
            outage_label = f"{outage_mcc.cluster_name}/idx={outage_idx}"
            outage_core_v1 = outage_mcc.core_v1_api()
            envoy_label_value = f"{MDBS_NAME}-search-lb-{outage_idx}"
            logger.info(f"=== {outage_label}: starting envoy-restart scenario ===")

            tool = _direct_user_connectivity_tool(namespace, outage_idx)
            captured_uids: dict[str, str] = {}

            def fault():
                captured_uids.update(hard_kill_pods_by_label(outage_core_v1, namespace, "app", envoy_label_value))

            _, _, verdict = paging_baseline_and_fault(tool, fault_fn=fault)
            assert_disruption_observed(verdict, context=f"{outage_label}: cluster envoy restart")

            for other_mcc in member_cluster_clients:
                if other_mcc.cluster_name == outage_mcc.cluster_name:
                    continue
                other_idx = helper.cluster_index(other_mcc.cluster_name)
                other_result = _direct_user_connectivity_tool(namespace, other_idx).oneshot_search()
                assert other_result.success, (
                    f"cross-cluster survival: cluster {other_idx} should serve while "
                    f"{outage_label} envoy is restarting, got {other_result}"
                )
                logger.info(
                    f"cross-cluster survival OK: {other_mcc.cluster_name}/idx={other_idx} "
                    f"served during {outage_label} envoy restart"
                )

            wait_for_pods_by_label_replaced(
                namespace,
                f"app={envoy_label_value}",
                captured_uids,
                api_client=outage_mcc.api_client,
                timeout=180,
            )
            logger.info(f"{outage_label}: envoy pods replaced, scenario complete")

    def test_single_cluster_mongot_outage_preserves_other_clusters(
        self,
        namespace: str,
        central_cluster_client: kubernetes.client.ApiClient,
        member_cluster_clients: List[MultiClusterClient],
    ):
        """Scale ONE cluster's mongot to 0 while a dual-mode (oneshot + paging) fleet runs
        per cluster: the faulted cluster surfaces an outage in both modes, the others serve
        continuously."""
        helper = _mc_helper(namespace, member_cluster_clients)
        mdbs = _load_mdbs(namespace, central_cluster_client)
        original_clusters = [dict(c) for c in mdbs["spec"]["clusters"]]

        outage_mcc = member_cluster_clients[0]
        outage_idx = helper.cluster_index(outage_mcc.cluster_name)
        sts_name = f"{MDBS_NAME}-search-{outage_idx}"
        indexes = [helper.cluster_index(mcc.cluster_name) for mcc in member_cluster_clients]

        with MultiClusterAvailabilityFleet(
            tester_factory=lambda idx: _direct_user_connectivity_tool(namespace, idx),
            cluster_indexes=indexes,
            interval_seconds=0.2,
        ) as fleet:
            fleet.wait_for_operations_all(5)
            try:
                mdbs.load()
                clusters = [dict(c) for c in original_clusters]
                for entry in clusters:
                    if entry["name"] == outage_mcc.cluster_name:
                        entry["replicas"] = 0
                mdbs["spec"]["clusters"] = clusters
                mdbs.update()
                logger.info(f"scaled cluster {outage_idx} ({outage_mcc.cluster_name}) mongot -> 0")
                wait_for_mongot_statefulset_drained(sts_name, namespace, api_client=outage_mcc.api_client, timeout=300)
                fleet.wait_for_operations_all(40)  # observe through the outage
            finally:
                # Wait out the async PVC GC before restoring replicas (see
                # wait_for_mongot_pvcs_deleted docstring for the STS-controller race).
                wait_for_mongot_pvcs_deleted(
                    namespace,
                    sts_name,
                    api_client=outage_mcc.api_client,
                    timeout=300,
                )
                mdbs.load()
                mdbs["spec"]["clusters"] = [dict(c) for c in original_clusters]
                mdbs.update()
                # The fresh per-cluster replica must reindex from mongod before it
                # reports Ready, so allow a generous window.
                mdbs.assert_reaches_phase(Phase.Running, timeout=1200)
                logger.info(f"restored cluster {outage_idx} mongot replicas, MDBS back to Running")

        fleet.assert_single_cluster_outage(outage_idx)


def _mc_helper(namespace: str, member_cluster_clients: List[MultiClusterClient]) -> MCSearchDeploymentHelper:
    return MCSearchDeploymentHelper(
        namespace=namespace,
        mdbs_resource_name=MDBS_NAME,
        member_cluster_clients={mcc.cluster_name: mcc.core_v1_api() for mcc in member_cluster_clients},
    )


def _load_mdbs(namespace: str, central_cluster_client: kubernetes.client.ApiClient) -> MongoDBSearch:
    resource = MongoDBSearch(name=MDBS_NAME, namespace=namespace)
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    resource.load()
    return resource


def _user_connectivity_tool(namespace: str) -> SearchConnectivityTool:
    return SearchConnectivityTool(mc_rs_search_tester(MDBS_NAME, namespace, MDB.user_name, MDB.user_password))


def _direct_user_connectivity_tool(namespace: str, cluster_index: int, member_index: int = 0) -> SearchConnectivityTool:
    return SearchConnectivityTool(
        mc_rs_search_tester_for_cluster_member(
            MDBS_NAME, namespace, cluster_index, member_index, MDB.user_name, MDB.user_password
        )
    )
