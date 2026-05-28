"""E2E tests for the search connectivity background tester (KUBE-27).

Three scenarios exercise the tester end-to-end:

* ``test_no_outage`` — steady-state observation window; the verdict must
  show zero failures.
* ``test_mongot_pod_restart_surfaces_cursor_lost`` — kill mongot pods
  mid-paging; the open cursor cannot survive the replacement.
* ``test_mongot_scale_to_zero_surfaces_network_error`` — drain the mongot
  StatefulSet entirely; every probe must fail with a network class.
"""

from __future__ import annotations

import time

import pytest
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
    SearchDeploymentTests,
    SearchE2EFixtures,
    SearchSampleDataAndIndexTests,
    _derive_user_defaults,
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

NO_OUTAGE_OBSERVATION_SECONDS = 20.0
HEALTHY_BASELINE_SECONDS = 3.0
FAULT_OBSERVATION_SECONDS = 45.0


def configure_mongodb_rs_config(cfg: MongoDBRsDeploymentConfig) -> MongoDBRsDeploymentConfig:
    cfg.mdb_resource_name = "mdb-rs-bg-tester"
    cfg.admin_user_name = ""
    cfg.admin_user_password = ""
    cfg.user_name = ""
    cfg.user_password = ""
    _derive_user_defaults(cfg)
    return cfg


def _new_tester(mdb: MongoDB, user_name: str, user_password: str) -> SearchAvailabilityBackgroundTester:
    search_tester = get_rs_search_tester(mdb, user_name, user_password, use_ssl=True)
    tool = SearchConnectivityTool(search_tester)
    return SearchAvailabilityBackgroundTester(tool)


class TestSearchWithReplicaSet(
    SearchDeploymentTests,
    MongoDBRsDeploymentTests,
):
    def build_mongodb_rs_config(self) -> MongoDBRsDeploymentConfig:
        return configure_mongodb_rs_config(super().build_mongodb_rs_config())


class TestSearchSampleDataAndIndex(
    SearchSampleDataAndIndexTests,
    SearchE2EFixtures,
):
    def build_mongodb_rs_config(self) -> MongoDBRsDeploymentConfig:
        return configure_mongodb_rs_config(super().build_mongodb_rs_config())


class TestSearchConnectivityBackgroundTester(SearchE2EFixtures):
    def build_mongodb_rs_config(self) -> MongoDBRsDeploymentConfig:
        return configure_mongodb_rs_config(super().build_mongodb_rs_config())

    def test_no_outage(self, mdb: MongoDB):
        cfg = self.build_mongodb_rs_config()
        with _new_tester(mdb, cfg.user_name, cfg.user_password) as tester:
            time.sleep(NO_OUTAGE_OBSERVATION_SECONDS)
        verdict = tester.verdict
        logger.info(f"no-outage verdict: {verdict.as_dict()}")
        assert_no_outage(verdict)

    def test_mongot_pod_restart_surfaces_cursor_lost(
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
        with _new_tester(mdb, cfg.user_name, cfg.user_password) as tester:
            time.sleep(HEALTHY_BASELINE_SECONDS)
            original_uids = delete_pods(namespace, label_selector=pod_selector)
            time.sleep(FAULT_OBSERVATION_SECONDS)

        try:
            wait_for_pods_by_label_replaced(namespace, pod_selector, original_uids)
        except Exception as e:
            logger.warning(f"mongot pod recreation wait timed out in cleanup: {e}")

        verdict = tester.verdict
        logger.info(f"mongot-restart verdict: {verdict.as_dict()}")
        assert_outage_detected(verdict, accept_classes=("cursor_lost",))

    def test_mongot_scale_to_zero_surfaces_network_error(
        self,
        mdb: MongoDB,
        mdbs: MongoDBSearch,
        namespace: str,
    ):
        cfg = self.build_mongodb_rs_config()
        statefulset_name = search_resource_names.mongot_statefulset_name(mdbs.name)

        with _new_tester(mdb, cfg.user_name, cfg.user_password) as tester:
            time.sleep(HEALTHY_BASELINE_SECONDS)
            logger.info(f"scaling MongoDBSearch {mdbs.name} replicas -> 0")
            mdbs["spec"]["replicas"] = 0
            mdbs.update()
            wait_for_mongot_statefulset_drained(statefulset_name, namespace)
            time.sleep(FAULT_OBSERVATION_SECONDS)

        logger.info(f"scaling MongoDBSearch {mdbs.name} replicas -> 1")
        mdbs["spec"]["replicas"] = 1
        mdbs.update()
        mdbs.assert_reaches_phase(Phase.Running, timeout=300)

        verdict = tester.verdict
        logger.info(f"scale-to-zero verdict: {verdict.as_dict()}")
        assert_outage_detected(verdict, accept_classes=("transient_network",))
