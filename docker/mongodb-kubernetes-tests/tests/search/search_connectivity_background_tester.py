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
    InstallOperatorTests,
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


def build_bg_tester_rs_config() -> MongoDBRsDeploymentConfig:
    # User names auto-derive from mdb_resource_name in __post_init__.
    return MongoDBRsDeploymentConfig(mdb_resource_name="mdb-rs-bg-tester")




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
        return build_bg_tester_rs_config()

    def build_search_deployment_config(self) -> SearchDeploymentConfig:
        return configure_search_deployment_config(super().build_search_deployment_config())


class TestSearchSampleDataAndIndex(
    SearchSampleDataAndIndexTests,
    SearchE2EFixtures,
):
    def build_mongodb_rs_config(self) -> MongoDBRsDeploymentConfig:
        return build_bg_tester_rs_config()


