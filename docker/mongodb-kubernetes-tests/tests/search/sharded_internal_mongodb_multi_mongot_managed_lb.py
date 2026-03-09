"""
E2E test for sharded MongoDB Search with managed L7 load balancer configuration.

This test verifies the sharded Search + managed LB PoC implementation:
- Deploys a sharded MongoDB cluster with TLS enabled
- Deploys MongoDBSearch with lb.mode: Managed (operator auto-deploys Envoy proxy)
- Verifies the operator-managed Envoy proxy deployment and configuration
- Verifies per-shard mongot Services are created
- Verifies per-shard mongot StatefulSets are created
- Verifies mongod parameters are set correctly for each shard (pointing to Envoy proxy)
- Verifies mongos search parameters are configured
- Imports sample data and shards collections
- Creates text and vector search indexes
- Executes search queries through mongos and verifies results from all shards
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
from tests.common.search.movies_search_helper import EmbeddedMoviesSearchHelper
from tests.common.search.search_deployment_helper import SearchDeploymentHelper
from tests.common.search.sharded_search_helper import *
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
MONGOT_PORT = 27028
ENVOY_PROXY_PORT = 27029
ENVOY_ADMIN_PORT = 9901

# Resource names
MDB_RESOURCE_NAME = "mdb-sh-managed-lb"
MDBS_RESOURCE_NAME = MDB_RESOURCE_NAME
SHARD_COUNT = 2
MONGODS_PER_SHARD = 1
MONGOS_COUNT = 1
CONFIG_SERVER_COUNT = 1

# TLS configuration
# Per-shard mongot TLS: {prefix}-{name}-search-0-{shardName}-cert (e.g., certs-mdb-sh-search-0-mdb-sh-0-cert)
# Managed LB server TLS: {prefix}-{name}-search-lb-cert (e.g., certs-mdb-sh-search-lb-cert)
# Managed LB client TLS: {prefix}-{name}-search-lb-client-cert (e.g., certs-mdb-sh-search-lb-client-cert)
MDBS_TLS_CERT_PREFIX = "certs"
CA_CONFIGMAP_NAME = f"{MDB_RESOURCE_NAME}-ca"


@fixture(scope="module")
def sharded_ca_configmap(issuer_ca_filepath: str, namespace: str) -> str:
    return create_sharded_ca(issuer_ca_filepath, namespace, CA_CONFIGMAP_NAME)


@fixture(scope="function")
def helper(namespace: str) -> SearchDeploymentHelper:
    return SearchDeploymentHelper(
        namespace=namespace,
        mdb_resource_name=MDB_RESOURCE_NAME,
        mdbs_resource_name=MDBS_RESOURCE_NAME,
    )


@fixture(scope="function")
def mdb(namespace: str, sharded_ca_configmap: str, helper: SearchDeploymentHelper) -> MongoDB:
    return helper.create_sharded_mdb(set_tls_ca=True)


@fixture(scope="function")
def mdbs(namespace: str) -> MongoDBSearch:
    resource = MongoDBSearch.from_yaml(
        yaml_fixture("search-sharded-managed-lb.yaml"),
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


@mark.e2e_search_sharded_enterprise_managed_lb
def test_install_operator(namespace: str, operator_installation_config: dict[str, str]):
    """Test that the operator is installed and running."""
    operator = get_default_operator(namespace, operator_installation_config=operator_installation_config)
    operator.assert_is_running()


@mark.e2e_search_sharded_enterprise_managed_lb
@skip_if_cloud_manager
def test_create_ops_manager(namespace: str):
    """Test OpsManager deployment (skipped for Cloud Manager)."""
    ops_manager = get_ops_manager(namespace)
    ops_manager.update()
    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=1200)
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_search_sharded_enterprise_managed_lb
def test_install_tls_certificates(helper: SearchDeploymentHelper, mdb: MongoDB, issuer: str):
    helper.install_sharded_tls_certificates()


@mark.e2e_search_sharded_enterprise_managed_lb
def test_create_sharded_cluster(mdb: MongoDB):
    """Test sharded cluster deployment."""
    mdb.update()
    mdb.assert_reaches_phase(Phase.Running, timeout=900)


@mark.e2e_search_sharded_enterprise_managed_lb
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


@mark.e2e_search_sharded_enterprise_managed_lb
def test_deploy_lb_certificates(namespace: str, issuer: str):
    """Create TLS certificates for the operator-managed load balancer."""
    create_lb_certificates(namespace, issuer, SHARD_COUNT, MDB_RESOURCE_NAME, MDBS_RESOURCE_NAME, MDBS_TLS_CERT_PREFIX)


@mark.e2e_search_sharded_enterprise_managed_lb
def test_create_search_tls_certificate(namespace: str, issuer: str):
    """Create per-shard TLS certificates for MongoDBSearch resource.

    Creates one certificate per shard with naming pattern:
    {prefix}-{name}-search-0-{shardName}-cert (e.g., certs-mdb-sh-search-0-mdb-sh-0-cert)

    The operator will create operator-managed secrets:
    {shardName}-search-certificate-key (e.g., mdb-sh-0-search-certificate-key)
    """
    create_per_shard_search_tls_certs(
        namespace, issuer, MDBS_TLS_CERT_PREFIX, SHARD_COUNT, MDB_RESOURCE_NAME, MDBS_RESOURCE_NAME
    )


@mark.e2e_search_sharded_enterprise_managed_lb
def test_create_search_resource(mdbs: MongoDBSearch):
    """Test MongoDBSearch resource deployment with managed LB config."""
    mdbs.update()
    mdbs.assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_search_sharded_enterprise_managed_lb
def test_verify_envoy_deployment(namespace: str):
    envoy_deployment_name = search_resource_names.lb_deployment_name(MDB_RESOURCE_NAME)

    # Verify Envoy Deployment is running (with polling)
    def check_envoy_deployment():
        try:
            apps_v1 = client.AppsV1Api()
            deployment = apps_v1.read_namespaced_deployment(envoy_deployment_name, namespace)
            ready = deployment.status.ready_replicas or 0
            return ready >= 1, f"ready_replicas={ready}"
        except Exception as e:
            return False, f"Deployment {envoy_deployment_name} not found: {e}"

    run_periodically(check_envoy_deployment, timeout=120, sleep_time=5, msg=f"Envoy Deployment {envoy_deployment_name}")
    logger.info(f"✓ Envoy Deployment {envoy_deployment_name} is running")


@mark.e2e_search_sharded_enterprise_managed_lb
def test_wait_for_sharded_cluster_ready(mdb: MongoDB):
    """Wait for sharded cluster to be ready after Search CR deployment."""
    mdb.assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_search_sharded_enterprise_managed_lb
def test_verify_mongod_parameters_per_shard(namespace: str, mdb: MongoDB, mdbs: MongoDBSearch):
    verify_sharded_mongod_parameters(
        namespace,
        MDB_RESOURCE_NAME,
        mdbs.name,
        SHARD_COUNT,
        expected_host_fn=lambda shard: search_resource_names.shard_proxy_service_host(
            mdbs.name, shard, namespace, ENVOY_PROXY_PORT
        ),
    )


# TODO: We don't really need this, it can be removed if we have a way to figure out a logical time
# to wait for to get the mongod/mongos config properly generated.
@mark.e2e_search_sharded_enterprise_managed_lb
def test_verify_mongos_search_config(namespace: str, mdb: MongoDB):
    verify_mongos_search_config(namespace, MDB_RESOURCE_NAME)


@mark.e2e_search_sharded_enterprise_managed_lb
def test_deploy_tools_pod(tools_pod: mongodb_tools_pod.ToolsPod):
    """Deploy mongodb-tools pod for running queries."""
    # The tools_pod fixture handles deployment and waiting for readiness
    logger.info(f"✓ Tools pod {tools_pod.pod_name} is ready")


@mark.e2e_search_sharded_enterprise_managed_lb
def test_restore_sample_database(mdb: MongoDB, tools_pod: mongodb_tools_pod.ToolsPod):
    """Restore sample_mflix database to the sharded cluster.

    Uses mongorestore from inside the tools pod since the MongoDB cluster
    is only accessible via Kubernetes internal DNS.
    """
    search_tester = get_search_tester(mdb, ADMIN_USER_NAME, ADMIN_USER_PASSWORD, use_ssl=True)
    search_tester.mongorestore_from_url(
        archive_url="https://atlas-education.s3.amazonaws.com/sample_mflix.archive",
        ns_include="sample_mflix.*",
        tools_pod=tools_pod,
    )
    logger.info("✓ Sample database restored")


@mark.e2e_search_sharded_enterprise_managed_lb
def test_search_shard_collections(mdb: MongoDB):
    search_tester = get_search_tester(mdb, ADMIN_USER_NAME, ADMIN_USER_PASSWORD, use_ssl=True)
    search_tester.shard_and_distribute_collection("sample_mflix", "movies")
    logger.info("Collections sharded and chunks are distributed")


@mark.e2e_search_sharded_enterprise_managed_lb
def test_create_search_index(mdb: MongoDB):
    """Create text search index on movies collection.

    Uses SearchTester with direct pymongo connection. This works locally with kubefwd
    because pymongo (Python) properly resolves /etc/hosts entries, unlike Go-based tools.
    """
    search_tester = get_search_tester(mdb, USER_NAME, USER_PASSWORD, use_ssl=True)
    search_tester.create_search_index("sample_mflix", "movies")
    search_tester.wait_for_search_indexes_ready("sample_mflix", "movies", timeout=300)
    logger.info("✓ Text search index created")

    emb_helper = EmbeddedMoviesSearchHelper(search_tester)
    emb_helper.create_vector_search_index()
    emb_helper.wait_for_vector_search_index()
    logger.info("✓ Vector search index created on embedded_movies")


@mark.e2e_search_sharded_enterprise_managed_lb
def test_execute_text_search_query(mdb: MongoDB):
    search_tester = get_search_tester(mdb, USER_NAME, USER_PASSWORD, use_ssl=True)
    verify_text_search_query(search_tester)


@mark.e2e_search_sharded_enterprise_managed_lb
def test_verify_search_results_from_all_shards(mdb: MongoDB):
    search_tester = get_search_tester(mdb, USER_NAME, USER_PASSWORD, use_ssl=True)
    verify_search_results_from_all_shards(search_tester)


@mark.e2e_search_sharded_enterprise_managed_lb
def test_vector_search_before_and_after_sharding(mdb: MongoDB):
    search_tester = get_search_tester(mdb, USER_NAME, USER_PASSWORD, use_ssl=True)
    admin_search_tester = get_search_tester(mdb, ADMIN_USER_NAME, ADMIN_USER_PASSWORD, use_ssl=True)
    verify_vector_search_before_and_after_sharding(search_tester, admin_search_tester)


@mark.e2e_search_sharded_enterprise_managed_lb
def test_verify_search_resource_status(mdbs: MongoDBSearch):
    """Verify the MongoDBSearch resource is in Running phase with correct status."""
    mdbs.load()

    phase = mdbs.get_status_phase()
    assert phase == Phase.Running, f"MongoDBSearch phase is {phase}, expected Running"

    logger.info(f"✓ MongoDBSearch {mdbs.name} is in Running phase")
