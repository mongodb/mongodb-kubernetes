"""E2E for the search connectivity tool against a 3-cluster MongoDBMulti RS
"""

from __future__ import annotations

from typing import List

import pytest
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.mongodb_search import MongoDBSearch
from kubetester.multicluster_client import MultiClusterClient
from kubetester.phase import Phase
from tests import test_logger
from tests.common.search.bootstrap_test_mixins import SearchSampleDataAndIndexTests
from tests.common.search.bootstrap_test_mixins_mc import (
    MongoDBMultiRsDeploymentConfig,
    MongoDBMultiRsDeploymentTests,
    SearchMCDeploymentTests,
    SearchMCE2EFixtures,
)
from tests.common.search.connectivity import (
    SearchConnectivityTool,
    assert_disruption_observed,
    hard_kill_pods_by_label,
    paging_baseline_and_fault,
    wait_for_mongot_statefulset_drained,
    wait_for_pods_by_label_replaced,
)
from tests.common.search.rs_search_helper import get_mc_rs_search_tester, get_mc_rs_search_tester_for_cluster_member
from tests.common.search.search_deployment_helper import MCSearchDeploymentHelper

logger = test_logger.get_test_logger(__name__)

pytestmark = pytest.mark.e2e_search_connectivity_tool_mc_rs


def build_conn_tool_mc_rs_config() -> MongoDBMultiRsDeploymentConfig:
    # User names auto-derive from mdb_resource_name in __post_init__.
    return MongoDBMultiRsDeploymentConfig(mdb_resource_name="mdb-mc-rs-conn-tool")


class TestSearchWithMongoDBMulti(
    # Bases listed in REVERSE execution order — pytest emits inherited
    # tests in reversed(MRO). See bootstrap_test_mixins module docstring.
    SearchMCDeploymentTests,
    MongoDBMultiRsDeploymentTests,
):
    def build_mongodb_mc_rs_config(self) -> MongoDBMultiRsDeploymentConfig:
        return build_conn_tool_mc_rs_config()


class TestSearchSampleDataAndIndex(
    # ``SearchSampleDataAndIndexTests`` is topology-agnostic (shared with
    # the SC RS / SC sharded tests). ``SearchMCE2EFixtures`` supplies the
    # MC-flavoured ``_admin_tester`` / ``_user_tester`` /
    # ``search_tools_pod`` hooks (tools pod lands in the first member cluster).
    SearchSampleDataAndIndexTests,
    SearchMCE2EFixtures,
):
    def build_mongodb_mc_rs_config(self) -> MongoDBMultiRsDeploymentConfig:
        return build_conn_tool_mc_rs_config()


# ---------------------------------------------------------------------------
# Connectivity-tool tests.
# ---------------------------------------------------------------------------


class TestSearchConnectivityToolMC(SearchMCE2EFixtures):
    def build_mongodb_mc_rs_config(self) -> MongoDBMultiRsDeploymentConfig:
        return build_conn_tool_mc_rs_config()

    """Exercises SearchConnectivityTool against the MC RS source.

    The primary-routed tests (oneshot / first-page paging) connect via a
    ``replicaSet=`` URI so the driver follows the primary across
    failovers — they only assert that mongod wire ops + verdict-level
    signals fire, not which cluster served the request.

    The per-cluster direct tests use ``directConnection=true`` so they
    always exercise the cluster they target, regardless of which cluster
    holds the primary.
    """

    # ------------------------------------------------------------------
    # Happy path — through the RS primary.
    # ------------------------------------------------------------------

    def test_oneshot_search_through_primary(self, mdb: MongoDBMulti):
        """One-shot search through the RS primary returns documents.

        ``mongod_wire_ops>0`` proves driver-to-mongod traffic; this test
        intentionally does not attribute which cluster's mongot served
        the query — the structural per-cluster routing invariant is
        proved by the bootstrap mixin (per-cluster ``mongotHost`` AC
        patch + per-cluster envoy SNI).
        """
        cfg = self.build_mongodb_mc_rs_config()
        tester = get_mc_rs_search_tester(mdb, cfg.user_name, cfg.user_password)
        tool = SearchConnectivityTool(tester)

        result = tool.oneshot_search()
        logger.info(f"oneshot_search result: {result}")
        assert result.success, f"one-shot search failed: {result.error_class} {result.error_message}"
        assert result.returned_count > 0, "expected results from cache-busted compound query"

    def test_paging_search_first_page_succeeds(self, mdb: MongoDBMulti):
        """First paging page returns documents (cursor opens, firstBatch lands)."""
        cfg = self.build_mongodb_mc_rs_config()
        tester = get_mc_rs_search_tester(mdb, cfg.user_name, cfg.user_password)
        tool = SearchConnectivityTool(tester)

        pages = tool.paging_search(pages=3, interval_seconds=0.1, batch_size=20)
        logger.info("paging_search: %s", "; ".join(str(p) for p in pages))
        assert pages, "paging_search returned no pages"
        assert pages[0].success, f"first page failed: {pages[0].error_class} {pages[0].error_message}"
        assert pages[0].returned_count > 0, "first page returned 0 docs"

    # ------------------------------------------------------------------
    # Per-cluster fan-out — fault paths.
    # ------------------------------------------------------------------

    def test_paging_through_per_cluster_mongot_outage(
        self,
        mdb: MongoDBMulti,
        mdbs: MongoDBSearch,
        namespace: str,
        member_cluster_clients: List[MultiClusterClient],
        helper: MCSearchDeploymentHelper,
    ):
        """For each cluster N: scale cluster N's mongot replicas to 0, wait for the
        STS to drain, then assert the direct-N tester surfaces disruption on a fresh
        cursor. Assert testers to other clusters keep serving. Then restore.

        Follows the SC test pattern (test_paging_through_mongot_outage_surfaces_
        connectivity_error): CRD patch → wait for drain → fresh cursor → assert failures.
        The fault is NOT applied mid-cursor because the operator reconcile latency is
        higher than the paging drain window.

        Per the MongoDBSearch CRD contract (api/v1/search/mongodbsearch_types.go),
        ``spec.clusters[i].replicas=0`` is explicitly supported — the operator
        scales cluster i's mongot StatefulSet to 0 cleanly. No resourceDisabled
        annotation or manual STS patching needed.
        """
        cfg = self.build_mongodb_mc_rs_config()
        # Capture per-cluster replicas as a fresh list-of-dicts so we can mutate
        # specific entries without disturbing the operator's cluster ordering.
        original_clusters = [dict(c) for c in mdbs["spec"]["clusters"]]

        for outage_mcc in member_cluster_clients:
            outage_idx = helper.cluster_index(outage_mcc.cluster_name)
            outage_label = f"{outage_mcc.cluster_name}/idx={outage_idx}"
            sts_name = f"{mdbs.name}-search-{outage_idx}"
            logger.info(f"=== {outage_label}: starting mongot-outage scenario ===")

            direct_tester = get_mc_rs_search_tester_for_cluster_member(
                mdb,
                outage_idx,
                0,
                cfg.user_name,
                cfg.user_password,
            )
            tool = SearchConnectivityTool(direct_tester)

            # Step 1: pre-confirm baseline is healthy.
            pre_pages = tool.paging_search(pages=2, interval_seconds=0.1, batch_size=10)
            logger.info(f"{outage_label}: pre-outage pages: %s", "; ".join(str(p) for p in pre_pages))
            assert any(p.success for p in pre_pages), f"{outage_label}: no pre-outage successful page"

            try:
                # Step 2: patch MDBS to set this cluster's replicas=0.
                mdbs.load()
                clusters = [dict(c) for c in original_clusters]
                for entry in clusters:
                    if entry["clusterName"] == outage_mcc.cluster_name:
                        entry["replicas"] = 0
                mdbs["spec"]["clusters"] = clusters
                mdbs.update()
                logger.info(f"{outage_label}: set spec.clusters replicas=0 for this cluster")

                # Step 3: wait for the cluster-N STS to scale to 0 (operator drains it).
                wait_for_mongot_statefulset_drained(
                    sts_name,
                    namespace,
                    timeout=300,
                )
                logger.info(f"{outage_label}: mongot STS {sts_name} has drained to 0 replicas")

                # Step 4: fresh paging cursor — at least one failure must surface.
                # 20 pages of headroom over mongod's pre-fault buffer.
                post_pages = tool.paging_search(pages=20, interval_seconds=0.5, batch_size=10)
                logger.info(f"{outage_label}: post-outage pages: %s", "; ".join(str(p) for p in post_pages))
                post_verdict = tool.verdict(post_pages)
                logger.info(f"{outage_label}: post-outage verdict: {post_verdict.as_dict()}")
                assert_disruption_observed(post_verdict, context=f"{outage_label}: cluster mongot outage")

                # Step 5: cross-cluster survival check — other clusters keep serving.
                for other_mcc in member_cluster_clients:
                    if other_mcc.cluster_name == outage_mcc.cluster_name:
                        continue
                    other_idx = helper.cluster_index(other_mcc.cluster_name)
                    other_tester = get_mc_rs_search_tester_for_cluster_member(
                        mdb,
                        other_idx,
                        0,
                        cfg.user_name,
                        cfg.user_password,
                    )
                    other_result = SearchConnectivityTool(other_tester).oneshot_search()
                    assert other_result.success, (
                        f"cross-cluster survival: cluster {other_idx} should serve while "
                        f"{outage_label} is out, got {other_result}"
                    )
                    logger.info(
                        f"cross-cluster survival OK: {other_mcc.cluster_name}/idx={other_idx} "
                        f"served while {outage_label} mongot was scaled to 0"
                    )
            finally:
                # Step 6: always restore per-cluster replicas so subsequent loop
                # iterations and tests start from a clean state.
                mdbs.load()
                mdbs["spec"]["clusters"] = [dict(c) for c in original_clusters]
                mdbs.update()
                mdbs.assert_reaches_phase(Phase.Running, timeout=600)
                logger.info(f"{outage_label}: restored per-cluster replicas, MDBS back to Running")

    def test_paging_through_per_cluster_envoy_restart(
        self,
        mdb: MongoDBMulti,
        mdbs: MongoDBSearch,
        namespace: str,
        member_cluster_clients: List[MultiClusterClient],
        helper: MCSearchDeploymentHelper,
    ):
        """For each cluster N: hard-kill cluster N's envoy pods, assert the
        direct-N tester surfaces disruption, assert testers to other clusters
        keep serving, then wait for the replacement envoy pods to come up.

        The per-cluster envoy Deployment name is ``{mdbs}-search-lb-0-{idx}``
        and its pods carry that as the ``app`` label — same convention as the
        SC envoy-restart test, just plumbed through the member-cluster client.
        """
        cfg = self.build_mongodb_mc_rs_config()
        for outage_mcc in member_cluster_clients:
            outage_idx = helper.cluster_index(outage_mcc.cluster_name)
            outage_label = f"{outage_mcc.cluster_name}/idx={outage_idx}"
            outage_core_v1 = outage_mcc.core_v1_api()
            envoy_label_value = f"{mdbs.name}-search-lb-0-{outage_idx}"
            logger.info(f"=== {outage_label}: starting envoy-restart scenario ===")

            direct_tester = get_mc_rs_search_tester_for_cluster_member(
                mdb,
                outage_idx,
                0,
                cfg.user_name,
                cfg.user_password,
            )
            tool = SearchConnectivityTool(direct_tester)

            captured_uids: dict[str, str] = {}

            def fault():
                captured_uids.update(hard_kill_pods_by_label(outage_core_v1, namespace, "app", envoy_label_value))

            _, _, verdict = paging_baseline_and_fault(tool, fault_fn=fault)
            assert_disruption_observed(verdict, context=f"{outage_label}: cluster envoy restart")

            # Cross-cluster survival check.
            for other_mcc in member_cluster_clients:
                if other_mcc.cluster_name == outage_mcc.cluster_name:
                    continue
                other_idx = helper.cluster_index(other_mcc.cluster_name)
                other_tester = get_mc_rs_search_tester_for_cluster_member(
                    mdb,
                    other_idx,
                    0,
                    cfg.user_name,
                    cfg.user_password,
                )
                other_result = SearchConnectivityTool(other_tester).oneshot_search()
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
                timeout=180,
            )
            logger.info(f"{outage_label}: envoy pods replaced, scenario complete")
