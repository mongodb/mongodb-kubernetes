"""Acceptance smoke: steady-state search availability (single-cluster RS).

Consumes the availability harness (`SearchAvailabilityBackgroundTester` +
`SearchConnectivityTool`) and the deployment mixins from `tests/common/search`.
Deploys an external-source replica set with the managed Envoy LB, seeds
sample_mflix + an index, then runs a background `$search` load and asserts no
outage under steady state (no disruption). Disruption scenarios extend this by
injecting a fault between starting the tester and taking the verdict.
"""

from __future__ import annotations

import pytest
from tests import test_logger
from tests.common.search.background_availability_tester import (
    SearchAvailabilityBackgroundTester,
    assert_no_outage,
)
from tests.common.search.bootstrap_test_mixins import (
    InstallOperatorTests,
    MongoDBDeploymentConfig,
    MongoDBRsDeploymentTests,
    SampleDataAndIndexConfig,
    SearchDeploymentConfig,
    SearchRsDeploymentTests,
    SearchSampleDataAndIndexTests,
)
from tests.common.search.connectivity import SearchConnectivityTool
from tests.common.search.rs_search_helper import rs_search_tester
from tests.conftest import get_namespace

logger = test_logger.get_test_logger(__name__)

pytestmark = pytest.mark.e2e_search_availability_smoke

NAMESPACE = get_namespace()
MDB = MongoDBDeploymentConfig(mdb_resource_name="mdb-rs-avail")
SEARCH = SearchDeploymentConfig()

# paging-mode operations to observe across the steady-state window
STEADY_STATE_OPERATIONS = 30


class TestInstallOperator(InstallOperatorTests):
    pass


class TestMongoDBDeployment(MongoDBRsDeploymentTests):
    namespace = NAMESPACE
    mdb_config = MDB
    search_config = SEARCH


class TestSearchDeployment(SearchRsDeploymentTests):
    namespace = NAMESPACE
    mdb_config = MDB
    search_config = SEARCH


class TestSampleData(SearchSampleDataAndIndexTests):
    sample_config = SampleDataAndIndexConfig()

    def admin_tester(self, namespace: str):
        return rs_search_tester(MDB.mdb_resource_name, namespace, MDB.admin_user_name, MDB.admin_user_password)

    def user_tester(self, namespace: str):
        return rs_search_tester(MDB.mdb_resource_name, namespace, MDB.user_name, MDB.user_password)


class TestSteadyStateAvailability:
    def test_steady_state_availability(self, namespace: str):
        tool = SearchConnectivityTool(
            rs_search_tester(MDB.mdb_resource_name, namespace, MDB.user_name, MDB.user_password)
        )
        with SearchAvailabilityBackgroundTester(tool, mode="paging") as bg:
            bg.wait_for_operations(STEADY_STATE_OPERATIONS)
        assert_no_outage(bg.verdict)
