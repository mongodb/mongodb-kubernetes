"""
E2E test for ReplicaSet MongoDB Search with managed L7 load balancer.

This test verifies the RS + managed LB implementation:
- Deploys an Enterprise RS MongoDB cluster with TLS
- Deploys MongoDBSearch with lb.mode: Managed and replicas: 2
- Verifies the operator-managed Envoy proxy deployment
- Verifies mongod parameters point to the Envoy proxy service (port 27029)
- Imports sample data, creates search indexes, and executes search queries
"""

from kubernetes import client
from kubetester import try_load
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.kubetester import run_periodically
from kubetester.mongodb import MongoDB
from kubetester.mongodb_search import MongoDBSearch
from kubetester.mongodb_user import MongoDBUser
from kubetester.omtester import skip_if_cloud_manager
from kubetester.phase import Phase
from pytest import fixture, mark
from tests import test_logger
from tests.common.mongodb_tools_pod import mongodb_tools_pod
from tests.common.search import search_resource_names
from tests.common.search.rs_search_helper import (
    create_rs_lb_certificates,
    create_rs_search_tls_cert,
    get_rs_search_tester,
    verify_rs_mongod_parameters,
)
from tests.common.search.search_deployment_helper import SearchDeploymentHelper
from tests.common.search.sharded_search_helper import create_issuer_ca, verify_text_search_query
from tests.conftest import get_default_operator
from tests.search.om_deployment import get_ops_manager

logger = test_logger.get_test_logger(__name__)

# User credentials
ADMIN_USER_NAME = "mdb-admin-user"
ADMIN_USER_PASSWORD = "mdb-admin-user-pass"

MONGOT_USER_NAME = "search-sync-source"
MONGOT_USER_PASSWORD = "search-sync-source-user-password"

USER_NAME = "mdb-user"
USER_PASSWORD = "mdb-user-pass"

# Ports
ENVOY_PROXY_PORT = 27029

# Resource names
MDB_RESOURCE_NAME = "mdb-rs-managed-lb"
MDBS_RESOURCE_NAME = MDB_RESOURCE_NAME
RS_MEMBERS = 3

# TLS configuration
MDBS_TLS_CERT_PREFIX = "certs"
CA_CONFIGMAP_NAME = f"{MDB_RESOURCE_NAME}-ca"


@fixture(scope="module")
def ca_configmap(issuer_ca_filepath: str, namespace: str) -> str:
    return create_issuer_ca(issuer_ca_filepath, namespace, CA_CONFIGMAP_NAME)


@fixture(scope="function")
def helper(namespace: str) -> SearchDeploymentHelper:
    return SearchDeploymentHelper(
        namespace=namespace,
        mdb_resource_name=MDB_RESOURCE_NAME,
        mdbs_resource_name=MDBS_RESOURCE_NAME,
        ca_configmap_name=CA_CONFIGMAP_NAME,
    )


@fixture(scope="function")
def mdb(namespace: str, ca_configmap: str, helper: SearchDeploymentHelper) -> MongoDB:
    return helper.create_rs_mdb(set_tls=True)


@fixture(scope="function")
def mdbs(namespace: str) -> MongoDBSearch:
    resource = MongoDBSearch.from_yaml(
        yaml_fixture("search-rs-managed-lb.yaml"),
        namespace=namespace,
        name=MDBS_RESOURCE_NAME,
    )

    if try_load(resource):
        return resource

    resource["spec"]["source"]["mongodbResourceRef"]["name"] = MDB_RESOURCE_NAME

    return resource


@fixture(scope="function")
def admin_user(helper: SearchDeploymentHelper) -> MongoDBUser:
    return helper.admin_user_resource(ADMIN_USER_NAME)


@fixture(scope="function")
def user(helper: SearchDeploymentHelper) -> MongoDBUser:
    return helper.user_resource(USER_NAME)


@fixture(scope="function")
def mongot_user(helper: SearchDeploymentHelper, mdbs: MongoDBSearch) -> MongoDBUser:
    return helper.mongot_user_resource(mdbs, MONGOT_USER_NAME)


@mark.e2e_search_rs_enterprise_managed_lb
def test_install_operator(namespace: str, operator_installation_config: dict[str, str]):
    operator = get_default_operator(namespace, operator_installation_config=operator_installation_config)
    operator.assert_is_running()


@mark.e2e_search_rs_enterprise_managed_lb
@skip_if_cloud_manager
def test_create_ops_manager(namespace: str):
    ops_manager = get_ops_manager(namespace)
    ops_manager.update()
    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=1200)
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_search_rs_enterprise_managed_lb
def test_install_tls_certificates(helper: SearchDeploymentHelper, mdb: MongoDB, issuer: str):
    helper.install_rs_tls_certificates(issuer, members=RS_MEMBERS)


@mark.e2e_search_rs_enterprise_managed_lb
def test_create_database_resource(mdb: MongoDB):
    mdb.update()
    mdb.assert_reaches_phase(Phase.Running, timeout=300)


@mark.e2e_search_rs_enterprise_managed_lb
def test_create_users(
    helper: SearchDeploymentHelper,
    admin_user: MongoDBUser,
    user: MongoDBUser,
    mongot_user: MongoDBUser,
    mdb: MongoDB,
):
    helper.deploy_users(
        admin_user,
        ADMIN_USER_PASSWORD,
        user,
        USER_PASSWORD,
        mongot_user,
        MONGOT_USER_PASSWORD,
    )


@mark.e2e_search_rs_enterprise_managed_lb
def test_deploy_lb_certificates(namespace: str, issuer: str):
    create_rs_lb_certificates(namespace, issuer, MDBS_RESOURCE_NAME, MDBS_TLS_CERT_PREFIX)


@mark.e2e_search_rs_enterprise_managed_lb
def test_create_search_tls_certificate(namespace: str, issuer: str):
    create_rs_search_tls_cert(namespace, issuer, MDBS_RESOURCE_NAME, MDBS_TLS_CERT_PREFIX)


@mark.e2e_search_rs_enterprise_managed_lb
def test_create_search_resource(mdbs: MongoDBSearch):
    mdbs.update()
    mdbs.assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_search_rs_enterprise_managed_lb
def test_verify_envoy_deployment(namespace: str):
    envoy_deployment_name = search_resource_names.lb_deployment_name(MDBS_RESOURCE_NAME)

    def check_envoy_deployment():
        try:
            apps_v1 = client.AppsV1Api()
            deployment = apps_v1.read_namespaced_deployment(envoy_deployment_name, namespace)
            ready = deployment.status.ready_replicas or 0
            return ready >= 1, f"ready_replicas={ready}"
        except Exception as e:
            return False, f"Deployment {envoy_deployment_name} not found: {e}"

    run_periodically(check_envoy_deployment, timeout=120, sleep_time=5, msg=f"Envoy Deployment {envoy_deployment_name}")
    logger.info(f"Envoy Deployment {envoy_deployment_name} is running")


@mark.e2e_search_rs_enterprise_managed_lb
def test_wait_for_database_ready(mdb: MongoDB):
    mdb.assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_search_rs_enterprise_managed_lb
def test_verify_mongod_parameters(namespace: str, mdb: MongoDB, mdbs: MongoDBSearch):
    expected_host = search_resource_names.lb_proxy_service_host(mdbs.name, namespace, ENVOY_PROXY_PORT)
    verify_rs_mongod_parameters(namespace, MDB_RESOURCE_NAME, RS_MEMBERS, expected_host)


@mark.e2e_search_rs_enterprise_managed_lb
def test_deploy_tools_pod(tools_pod: mongodb_tools_pod.ToolsPod):
    logger.info(f"Tools pod {tools_pod.pod_name} is ready")


@mark.e2e_search_rs_enterprise_managed_lb
def test_restore_sample_database(mdb: MongoDB, tools_pod: mongodb_tools_pod.ToolsPod):
    search_tester = get_rs_search_tester(mdb, ADMIN_USER_NAME, ADMIN_USER_PASSWORD, use_ssl=True)
    search_tester.mongorestore_from_url(
        archive_url="https://atlas-education.s3.amazonaws.com/sample_mflix.archive",
        ns_include="sample_mflix.*",
        tools_pod=tools_pod,
    )


@mark.e2e_search_rs_enterprise_managed_lb
def test_create_search_index(mdb: MongoDB):
    search_tester = get_rs_search_tester(mdb, USER_NAME, USER_PASSWORD, use_ssl=True)
    search_tester.create_search_index("sample_mflix", "movies")
    search_tester.wait_for_search_indexes_ready("sample_mflix", "movies", timeout=300)


@mark.e2e_search_rs_enterprise_managed_lb
def test_execute_text_search_query(mdb: MongoDB):
    search_tester = get_rs_search_tester(mdb, USER_NAME, USER_PASSWORD, use_ssl=True)
    verify_text_search_query(search_tester)


@mark.e2e_search_rs_enterprise_managed_lb
def test_verify_search_resource_status(mdbs: MongoDBSearch):
    mdbs.load()
    phase = mdbs.get_status_phase()
    assert phase == Phase.Running, f"MongoDBSearch phase is {phase}, expected Running"
    logger.info(f"MongoDBSearch {mdbs.name} is in Running phase")
