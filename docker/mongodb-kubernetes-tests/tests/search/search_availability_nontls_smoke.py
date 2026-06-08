"""Non-TLS search bootstrap smoke: proves the search_tls=False harness path deploys and serves.

Bootstraps a search deployment with search TLS disabled — source mongod ``searchTLSMode=disabled``,
MongoDBSearch CR without ``spec.security.tls`` (mongot serves plaintext gRPC, envoy plaintext, no
LB/search certs) — and asserts a clean steady-state window on both query types, plus that the CR
carries no TLS and the source mongod is on searchTLSMode=disabled. This is the base proof for the
non-TLS bootstrap that the off->on TLS-enable availability suite builds on.
"""

from __future__ import annotations

import pytest
from kubetester.mongodb import MongoDB
from kubetester.mongodb_search import MongoDBSearch
from tests import test_logger
from tests.common.search.background_availability_tester import SearchAvailabilityBackgroundTester, assert_no_outage
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

pytestmark = pytest.mark.e2e_search_availability_nontls_smoke

NAMESPACE = get_namespace()
MDB = MongoDBDeploymentConfig(mdb_resource_name="mdb-rs-nontls")
SEARCH = SearchDeploymentConfig(search_tls=False)

BASELINE_OPS = 30


def _user_tool(namespace: str) -> SearchConnectivityTool:
    return SearchConnectivityTool(rs_search_tester(MDB.mdb_resource_name, namespace, MDB.user_name, MDB.user_password))


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


class TestNonTLSSearchServes:
    def test_cr_has_no_search_tls(self, namespace: str):
        mdbs = MongoDBSearch(name=MDB.mdb_resource_name, namespace=namespace)
        mdbs.load()
        assert "tls" not in mdbs["spec"].get("security", {}), "non-TLS bootstrap unexpectedly set spec.security.tls"

    def test_mongod_search_tls_disabled(self, namespace: str):
        mdb = MongoDB(name=MDB.mdb_resource_name, namespace=namespace)
        mdb.load()
        mode = mdb["spec"]["additionalMongodConfig"]["setParameter"]["searchTLSMode"]
        assert mode == "disabled", f"non-TLS source mongod expected searchTLSMode=disabled, got {mode}"

    def test_steady_state(self, namespace: str):
        _user_tool(namespace).wait_for_sentinel_indexed(timeout=300)
        for mode in ("oneshot", "paging"):
            with SearchAvailabilityBackgroundTester(_user_tool(namespace), mode=mode, interval_seconds=0.1) as bg:
                bg.wait_for_operations(BASELINE_OPS)
            assert_no_outage(bg.verdict)
