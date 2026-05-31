"""E2E tests for the search connectivity background tester.
"""

from __future__ import annotations

import time

import pytest

from kubetester import wait_for_pods_ready
from kubetester.mongodb import MongoDB
from kubetester.mongodb_search import MongoDBSearch
from kubetester.phase import Phase
from tests import test_logger
from tests.common.search import search_resource_names
from tests.common.search.background_availability_tester import (
    SearchAvailabilityBackgroundTester,
    assert_no_outage,
    assert_outage_detected,
)
from tests.common.search.bootstrap_test_mixins import (
    MongoDBRsDeploymentConfig,
    MongoDBRsDeploymentTests,
    SearchDeploymentConfig,
    SearchDeploymentTests,
    SearchE2EFixtures,
    SearchSampleDataAndIndexTests,
    _derive_user_defaults, InstallOperatorTests,
)
from tests.common.search.connectivity import (
    SearchConnectivityTool,
    delete_pods,
    wait_for_mongot_statefulset_drained,
    wait_for_pods_by_label_replaced,
)
from tests.common.search.rs_search_helper import get_rs_search_tester

logger = test_logger.get_test_logger(__name__)

pytestmark = pytest.mark.e2e_search_connectivity_background_tester

FAULT_OBSERVATION_SECONDS = 45.0


def configure_mongodb_rs_config(cfg: MongoDBRsDeploymentConfig) -> MongoDBRsDeploymentConfig:
    cfg.mdb_resource_name = "mdb-rs-bg-tester"
    cfg.admin_user_name = ""
    cfg.admin_user_password = ""
    cfg.user_name = ""
    cfg.user_password = ""
    _derive_user_defaults(cfg)
    return cfg


def _new_one_shot_background_tester(
    mdb: MongoDB, user_name: str, user_password: str
) -> SearchAvailabilityBackgroundTester:
    search_tester = get_rs_search_tester(mdb, user_name, user_password, use_ssl=True)
    tool = SearchConnectivityTool(search_tester)
    return SearchAvailabilityBackgroundTester(tool, mode="oneshot")


def _new_paging_background_tester_pinned_to_one_mongot(
    mdb: MongoDB, user_name: str, user_password: str
) -> SearchAvailabilityBackgroundTester:
    search_tester = get_rs_search_tester(mdb, user_name, user_password, use_ssl=True)
    tool = SearchConnectivityTool(search_tester)
    return SearchAvailabilityBackgroundTester(tool, mode="paging", paging_reset_every=None)


def configure_search_deployment_config(cfg: SearchDeploymentConfig) -> SearchDeploymentConfig:
    cfg.mongot_replicas = 1
    return cfg


class TestInstallOperator(InstallOperatorTests):
    pass

class TestSearchWithReplicaSet(
    SearchDeploymentTests,
    MongoDBRsDeploymentTests,
):
    def build_mongodb_rs_config(self) -> MongoDBRsDeploymentConfig:
        return configure_mongodb_rs_config(super().build_mongodb_rs_config())

    def build_search_deployment_config(self) -> SearchDeploymentConfig:
        return configure_search_deployment_config(super().build_search_deployment_config())


class TestSearchSampleDataAndIndex(
    SearchSampleDataAndIndexTests,
    SearchE2EFixtures,
):
    def build_mongodb_rs_config(self) -> MongoDBRsDeploymentConfig:
        return configure_mongodb_rs_config(super().build_mongodb_rs_config())


class TestSearchConnectivityBackgroundTester(SearchE2EFixtures):
    def build_mongodb_rs_config(self) -> MongoDBRsDeploymentConfig:
        return configure_mongodb_rs_config(super().build_mongodb_rs_config())

    def test_scale_up_down_mongot_pods_without_outage_to_existing_mongot_nodes(self, mdb: MongoDB, mdbs: MongoDBSearch):
        cfg = self.build_mongodb_rs_config()
        mdbs.assert_reaches_phase(Phase.Running)
        initial_replicas_count = mdbs["spec"]["replicas"]
        pod_selector = f"app={search_resource_names.mongot_service_name(mdbs.name)}"
        # Per-event drain target: paging batch_size=5 × 200 pages = 1000 docs
        # past the scale event — enough that the paging cursor's prefetch
        # buffer is reissued at least once via real getMore round-trips.
        drain_min_pages = 200
        drain_timeout = 180.0
        # oneshot tester will not be pinned by design, so scaled up mongot nodes should work correctly as soon as those are ready
        with _new_one_shot_background_tester(mdb, cfg.user_name, cfg.user_password) as oneshot_tester:
            # paging background tester will use one of the replicas that existed before the test was executed
            # all the queries should be paging through that node and adding+removing new mongot node should not affect existing queries
            with _new_paging_background_tester_pinned_to_one_mongot(
                mdb, cfg.user_name, cfg.user_password
            ) as paging_tester:
                # warm both testers — first iteration sets up cursors and routes.
                paging_tester.wait_for_pages(5)
                oneshot_tester.wait_for_pages(5)

                # scale up: add a new mongot replica. Existing paging cursor must
                # stay pinned to its original mongot; oneshot must keep routing.
                paging_baseline = paging_tester.succeeded_count
                oneshot_baseline = oneshot_tester.succeeded_count
                mdbs["spec"]["replicas"] = initial_replicas_count + 1
                mdbs.update()
                mdbs.assert_reaches_phase(Phase.Running)
                wait_for_pods_ready(
                    mdbs.namespace, label_selector=pod_selector, expected_count=initial_replicas_count + 1
                )

                # Drain enough post-scale pages to be confident the cursor's
                # getMore hit mongot at least once — proves availability through
                # the scale-up, not just buffered docs.
                paging_tester.wait_for_pages(drain_min_pages, since=paging_baseline, timeout=drain_timeout)
                oneshot_tester.wait_for_pages(drain_min_pages, since=oneshot_baseline, timeout=drain_timeout)

                # scale down: remove the extra mongot. Cursor must survive.
                paging_baseline = paging_tester.succeeded_count
                oneshot_baseline = oneshot_tester.succeeded_count
                mdbs["spec"]["replicas"] = initial_replicas_count
                mdbs.update()
                mdbs.assert_reaches_phase(Phase.Running)
                wait_for_pods_ready(mdbs.namespace, label_selector=pod_selector, expected_count=initial_replicas_count)

                paging_tester.wait_for_pages(drain_min_pages, since=paging_baseline, timeout=drain_timeout)
                oneshot_tester.wait_for_pages(drain_min_pages, since=oneshot_baseline, timeout=drain_timeout)

        oneshot_verdict = oneshot_tester.verdict
        paging_verdict = paging_tester.verdict
        logger.info(f"no-outage oneshot verdict: {oneshot_verdict.as_dict()}")
        logger.info(f"no-outage paging verdict: {paging_verdict.as_dict()}")
        # Both testers must stay clean — envoy must only route to mongot
        # pods that are fully ready (post-INITIAL_SYNC). Any failure here
        # indicates the readiness probe or the service's
        # publishNotReadyAddresses setting is letting traffic reach an
        # incomplete mongot.
        assert_no_outage(oneshot_verdict)
        assert_no_outage(paging_verdict)

    def test_mongot_pod_restart_surfaces_outage(
        self,
        mdb: MongoDB,
        mdbs: MongoDBSearch,
        namespace: str,
    ):
        cfg = self.build_mongodb_rs_config()
        statefulset_name = search_resource_names.mongot_statefulset_name(mdbs.name)
        # Matches the headless-service selector — catches every mongot replica.
        pod_selector = f"app={statefulset_name}-svc"

        original_uids: dict[str, str] = {}
        with _new_one_shot_background_tester(mdb, cfg.user_name, cfg.user_password) as oneshot_tester:
            with _new_paging_background_tester_pinned_to_one_mongot(
                mdb, cfg.user_name, cfg.user_password
            ) as paging_tester:
                # Deterministic baseline — both testers must have read some
                # successful pages before the fault is applied.
                oneshot_tester.wait_for_pages(5)
                paging_tester.wait_for_pages(5)
                original_uids = delete_pods(namespace, label_selector=pod_selector, grace_period_seconds=0)
                time.sleep(FAULT_OBSERVATION_SECONDS)

        try:
            wait_for_pods_by_label_replaced(namespace, pod_selector, original_uids)
        except Exception as e:
            logger.warning(f"mongot pod recreation wait timed out in cleanup: {e}")

        oneshot_verdict = oneshot_tester.verdict
        paging_verdict = paging_tester.verdict
        logger.info(f"mongot-restart oneshot verdict: {oneshot_verdict.as_dict()}")
        logger.info(f"mongot-restart paging verdict: {paging_verdict.as_dict()}")
        # Oneshot catches the in-flight no-upstream window → transient_network.
        # Paging catches the post-recovery getMore on a stream the new mongot
        # doesn't own → cursor_lost (with occasional transient_network at the
        # window edges).
        assert_outage_detected(oneshot_verdict, accept_classes=("transient_network",))
        assert_outage_detected(paging_verdict, accept_classes=("cursor_lost", "transient_network"))

    def test_mongot_scale_to_zero_surfaces_network_error(
        self,
        mdb: MongoDB,
        mdbs: MongoDBSearch,
        namespace: str,
    ):
        cfg = self.build_mongodb_rs_config()
        statefulset_name = search_resource_names.mongot_statefulset_name(mdbs.name)

        # Two-tester pattern empirically validates the buffer hypothesis:
        # oneshot must surface transient_network (every iteration is a fresh
        # $search that has to reach mongot); paging must stay clean because
        # the existing cursor is served from mongod's prefetch buffer the
        # whole no-upstream window.
        with _new_one_shot_background_tester(mdb, cfg.user_name, cfg.user_password) as oneshot_tester:
            with _new_paging_background_tester_pinned_to_one_mongot(
                mdb, cfg.user_name, cfg.user_password
            ) as paging_tester:
                oneshot_tester.wait_for_pages(5)
                paging_tester.wait_for_pages(5)
                logger.info(f"scaling MongoDBSearch {mdbs.name} replicas -> 0")
                mdbs["spec"]["replicas"] = 0
                mdbs.update()
                wait_for_mongot_statefulset_drained(statefulset_name, namespace)
                time.sleep(FAULT_OBSERVATION_SECONDS)

        logger.info(f"scaling MongoDBSearch {mdbs.name} replicas -> 1")
        mdbs["spec"]["replicas"] = 1
        mdbs.update()
        mdbs.assert_reaches_phase(Phase.Running, timeout=300)

        oneshot_verdict = oneshot_tester.verdict
        paging_verdict = paging_tester.verdict
        logger.info(f"scale-to-zero oneshot verdict: {oneshot_verdict.as_dict()}")
        logger.info(f"scale-to-zero paging verdict: {paging_verdict.as_dict()}")
        assert_outage_detected(oneshot_verdict, accept_classes=("transient_network",))
        assert_no_outage(paging_verdict)
