"""
E2E test for ReplicaSet MongoDB Search with external MongoDB source and multiple mongot instances.

This test verifies the ReplicaSet Search with external MongoDB source and unmanaged LB:
- Deploys a ReplicaSet MongoDB cluster with TLS enabled (simulating an external cluster)
- Deploys Envoy proxy for L7 load balancing mongot traffic
- Deploys MongoDBSearch with spec.source.external.hostAndPorts, replicas=2, and unmanaged LB
- Verifies mongod parameters point to the Envoy proxy endpoint
- Imports sample data, creates text and vector search indexes
- Executes search queries and verifies results

Key difference from replicaset_external_mongodb_single_mongot.py:
- This test deploys multiple mongot instances (replicas=2) with an Envoy LB
- The single-mongot test has replicas=1 and no LB
"""

from kubetester.certs import create_mongodb_tls_certs, create_tls_certs
from kubetester.mongodb import MongoDB
from kubetester.mongodb_search import MongoDBSearch
from kubetester.mongodb_user import MongoDBUser
from kubetester.omtester import skip_if_cloud_manager
from kubetester.phase import Phase
from pytest import fixture, mark
from tests import test_logger
from tests.common.mongodb_tools_pod import mongodb_tools_pod
from tests.common.search import search_resource_names
from tests.common.search.envoy_helpers import EnvoyProxy
from tests.common.search.replicaset_search_helper import verify_rs_mongod_parameters, verify_vector_search
from tests.common.search.search_deployment_helper import SearchDeploymentHelper
from tests.common.search.search_tester import SearchTester
from tests.common.search.sharded_search_helper import (
    create_sharded_ca,
    verify_search_results_from_all_shards,
    verify_text_search_query,
)
from tests.conftest import get_default_operator, get_issuer_ca_filepath
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
MONGOT_PORT = 27028
ENVOY_PROXY_PORT = 27029
ENVOY_ADMIN_PORT = 9901

# Resource names
MDB_RESOURCE_NAME = "mdb-rs-ext-multi"
MDBS_RESOURCE_NAME = MDB_RESOURCE_NAME
RS_MEMBERS = 3

# TLS configuration
CA_CONFIGMAP_NAME = f"{MDB_RESOURCE_NAME}-ca"
MDBS_TLS_SECRET_NAME = search_resource_names.mongot_tls_cert_name(MDB_RESOURCE_NAME)

# Envoy proxy
ENVOY_PROXY_SVC_NAME = "envoy-proxy-svc"


def get_rs_search_tester(mdb: MongoDB, username: str, password: str, use_ssl: bool = False) -> SearchTester:
    ca_path = get_issuer_ca_filepath() if use_ssl else None
    return SearchTester.for_replicaset(mdb, username, password, use_ssl=use_ssl, ca_path=ca_path)


@fixture(scope="module")
def ca_configmap(issuer_ca_filepath: str, namespace: str) -> str:
    return create_sharded_ca(issuer_ca_filepath, namespace, CA_CONFIGMAP_NAME)


@fixture(scope="function")
def helper(namespace: str) -> SearchDeploymentHelper:
    return SearchDeploymentHelper(
        namespace=namespace,
        mdb_resource_name=MDB_RESOURCE_NAME,
        mdbs_resource_name=MDBS_RESOURCE_NAME,
        ca_configmap_name=CA_CONFIGMAP_NAME,
    )


@fixture(scope="function")
def envoy(namespace: str) -> EnvoyProxy:
    return EnvoyProxy(
        namespace=namespace,
        ca_configmap_name=CA_CONFIGMAP_NAME,
        mdbs_resource_name=MDBS_RESOURCE_NAME,
        mongot_port=MONGOT_PORT,
        envoy_proxy_port=ENVOY_PROXY_PORT,
        envoy_admin_port=ENVOY_ADMIN_PORT,
    )


@fixture(scope="function")
def mdb(namespace: str, ca_configmap: str, issuer_ca_configmap: str, helper: SearchDeploymentHelper) -> MongoDB:
    mongot_host = f"{ENVOY_PROXY_SVC_NAME}.{namespace}.svc.cluster.local:{ENVOY_PROXY_PORT}"
    return helper.create_replicaset_mdb(
        mongot_host=mongot_host,
        issuer_ca_configmap=issuer_ca_configmap,
        tls_cert_prefix="certs",
    )


@fixture(scope="function")
def mdbs(namespace: str, mdb: MongoDB, helper: SearchDeploymentHelper) -> MongoDBSearch:
    resource = helper.mdbs_for_ext_rs_source(mongot_user_name=MONGOT_USER_NAME, rs_members=RS_MEMBERS)
    resource["spec"]["replicas"] = 2
    resource["spec"]["lb"] = {
        "mode": "Unmanaged",
        "endpoint": f"{ENVOY_PROXY_SVC_NAME}.{namespace}.svc.cluster.local:{ENVOY_PROXY_PORT}",
    }
    resource["spec"]["security"] = {"tls": {"certificateKeySecretRef": {"name": MDBS_TLS_SECRET_NAME}}}
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


@mark.e2e_search_external_rs_multi_mongot_unmanaged_lb
def test_install_operator(namespace: str, operator_installation_config: dict[str, str]):
    """Test that the operator is installed and running."""
    operator = get_default_operator(namespace, operator_installation_config=operator_installation_config)
    operator.assert_is_running()


@mark.e2e_search_external_rs_multi_mongot_unmanaged_lb
@skip_if_cloud_manager
def test_create_ops_manager(namespace: str):
    """Test OpsManager deployment (skipped for Cloud Manager)."""
    ops_manager = get_ops_manager(namespace)
    ops_manager.update()
    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=1200)
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_search_external_rs_multi_mongot_unmanaged_lb
def test_install_tls_certificates(namespace: str, mdb: MongoDB, mdbs: MongoDBSearch, issuer: str):
    """Create TLS certificates for MongoDB RS and mongot (replicas=2)."""
    create_mongodb_tls_certs(issuer, namespace, mdb.name, f"certs-{mdb.name}-cert", mdb.get_members())

    search_service_name = search_resource_names.mongot_service_name(mdbs.name)
    create_tls_certs(
        issuer,
        namespace,
        search_resource_names.mongot_statefulset_name(mdbs.name),
        replicas=2,
        service_name=search_service_name,
        additional_domains=[f"{search_service_name}.{namespace}.svc.cluster.local"],
        secret_name=MDBS_TLS_SECRET_NAME,
    )


@mark.e2e_search_external_rs_multi_mongot_unmanaged_lb
def test_create_database_resource(mdb: MongoDB):
    """Test ReplicaSet deployment."""
    mdb.update()
    mdb.assert_reaches_phase(Phase.Running, timeout=300)


@mark.e2e_search_external_rs_multi_mongot_unmanaged_lb
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


@mark.e2e_search_external_rs_multi_mongot_unmanaged_lb
def test_deploy_envoy_certificates(envoy: EnvoyProxy, issuer: str):
    envoy.create_certificates(issuer)


@mark.e2e_search_external_rs_multi_mongot_unmanaged_lb
def test_deploy_envoy_proxy(envoy: EnvoyProxy):
    """Deploy Envoy proxy for L7 load balancing."""
    envoy.deploy()


@mark.e2e_search_external_rs_multi_mongot_unmanaged_lb
def test_create_search_resource(mdbs: MongoDBSearch):
    """Test MongoDBSearch resource deployment with external RS source, replicas=2, and unmanaged LB."""
    mdbs.update()
    mdbs.assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_search_external_rs_multi_mongot_unmanaged_lb
def test_wait_for_database_resource_ready(mdb: MongoDB):
    """Wait for automation agents to be ready after Search deployment."""
    mdb.get_om_tester().wait_agents_ready()
    mdb.assert_reaches_phase(Phase.Running, timeout=300)


@mark.e2e_search_external_rs_multi_mongot_unmanaged_lb
def test_wait_for_mongod_parameters(mdb: MongoDB):
    """Verify each mongod has mongotHost and searchIndexManagementHostAndPort set."""
    verify_rs_mongod_parameters(mdb.namespace, mdb.name, mdb.get_members())


@mark.e2e_search_external_rs_multi_mongot_unmanaged_lb
def test_search_deploy_tools_pod(tools_pod: mongodb_tools_pod.ToolsPod):
    """Deploy mongodb-tools pod for running queries."""
    logger.info(f"Tools pod {tools_pod.pod_name} is ready")


@mark.e2e_search_external_rs_multi_mongot_unmanaged_lb
def test_search_restore_sample_database(mdb: MongoDB, tools_pod: mongodb_tools_pod.ToolsPod):
    """Restore sample_mflix database to the ReplicaSet cluster."""
    search_tester = get_rs_search_tester(mdb, ADMIN_USER_NAME, ADMIN_USER_PASSWORD, use_ssl=True)
    search_tester.mongorestore_from_url(
        archive_url="https://atlas-education.s3.amazonaws.com/sample_mflix.archive",
        ns_include="sample_mflix.*",
        tools_pod=tools_pod,
    )
    logger.info("Sample database restored")


@mark.e2e_search_external_rs_multi_mongot_unmanaged_lb
def test_search_create_search_index(mdb: MongoDB):
    """Create text search index on movies."""
    search_tester = get_rs_search_tester(mdb, USER_NAME, USER_PASSWORD, use_ssl=True)
    search_tester.create_search_index("sample_mflix", "movies")
    search_tester.wait_for_search_indexes_ready("sample_mflix", "movies", timeout=300)
    logger.info("Text search index created")


@mark.e2e_search_external_rs_multi_mongot_unmanaged_lb
def test_execute_text_search_query(mdb: MongoDB):
    search_tester = get_rs_search_tester(mdb, USER_NAME, USER_PASSWORD, use_ssl=True)
    verify_text_search_query(search_tester)


@mark.e2e_search_external_rs_multi_mongot_unmanaged_lb
def test_search_verify_all_results(mdb: MongoDB):
    search_tester = get_rs_search_tester(mdb, USER_NAME, USER_PASSWORD, use_ssl=True)
    verify_search_results_from_all_shards(search_tester)


@mark.e2e_search_external_rs_multi_mongot_unmanaged_lb
def test_vector_search(mdb: MongoDB):
    """Verify vector search works with multi-mongot and Envoy LB."""
    search_tester = get_rs_search_tester(mdb, USER_NAME, USER_PASSWORD, use_ssl=True)
    verify_vector_search(search_tester)


@mark.e2e_search_external_rs_multi_mongot_unmanaged_lb
def test_verify_search_resource_status(mdbs: MongoDBSearch):
    """Verify the MongoDBSearch resource is in Running phase."""
    mdbs.load()

    phase = mdbs.get_status_phase()
    assert phase == Phase.Running, f"MongoDBSearch phase is {phase}, expected Running"

    logger.info(f"MongoDBSearch {mdbs.name} is in Running phase")
