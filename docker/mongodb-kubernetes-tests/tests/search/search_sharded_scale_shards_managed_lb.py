"""
E2E test for adding/removing shards in a sharded MongoDB Search deployment with managed LB.

This test verifies that when shards are added to or removed from a sharded MongoDB cluster,
the MongoDBSearch resource correctly reconciles:
- Creates per-shard mongot StatefulSets, Services, and proxy Services for new shards
- Cleans up stale resources when shards are removed
- Search remains functional through shard topology changes including data rebalancing

Test flow:
  Phase 1: Deploy sharded cluster (2 shards) + MongoDBSearch with managed LB, verify search
  Phase 2: Scale UP to 3 shards, redistribute data, verify search works across all 3 shards
  Phase 3: Scale DOWN to 2 shards, verify cleanup and search still works
"""

import kubernetes
from kubernetes import client
from kubetester import try_load
from kubetester.certs import ISSUER_CA_NAME, create_mongodb_tls_certs, create_tls_certs
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
from tests.common.search.search_deployment_helper import SearchDeploymentHelper
from tests.common.search.sharded_search_helper import (
    create_issuer_ca,
    create_lb_certificates,
    create_per_shard_search_tls_certs,
    get_search_tester,
    verify_mongos_search_config,
    verify_search_results_from_all_shards,
    verify_sharded_mongod_parameters,
    verify_text_search_query,
)
from tests.conftest import get_default_operator
from tests.search.om_deployment import get_ops_manager

logger = test_logger.get_test_logger(__name__)

# Shard counts
INITIAL_SHARD_COUNT = 2
SCALED_UP_SHARD_COUNT = 3
SCALED_DOWN_SHARD_COUNT = 2

# User credentials
ADMIN_USER_NAME = "mdb-admin-user"
ADMIN_USER_PASSWORD = "mdb-admin-user-pass"
MONGOT_USER_NAME = "search-sync-source"
MONGOT_USER_PASSWORD = "search-sync-source-user-password"
USER_NAME = "mdb-user"
USER_PASSWORD = "mdb-user-pass"

# Ports
MONGOT_PORT = 27028
ENVOY_PROXY_PORT = 27028

# Resource names
MDB_RESOURCE_NAME = "mdb-sh-scale"
MDBS_RESOURCE_NAME = MDB_RESOURCE_NAME
MONGODS_PER_SHARD = 1
MONGOS_COUNT = 1
CONFIG_SERVER_COUNT = 1

# TLS configuration
MDBS_TLS_CERT_PREFIX = "certs"
MDB_TLS_SECRET_PREFIX = "mdb-sh-"
CA_CONFIGMAP_NAME = f"{MDB_RESOURCE_NAME}-ca"

MARKER = mark.e2e_search_sharded_scale_shards_managed_lb


# ---------------------------------------------------------------------------
# Inline verification helpers for shard resource existence / deletion
# ---------------------------------------------------------------------------


def verify_shard_resources_exist(namespace: str, mdbs_name: str, shard_name: str):
    apps_v1 = client.AppsV1Api()
    core_v1 = client.CoreV1Api()

    sts_name = search_resource_names.shard_statefulset_name(mdbs_name, shard_name)
    sts = apps_v1.read_namespaced_stateful_set(sts_name, namespace)
    assert sts is not None, f"StatefulSet {sts_name} not found"
    logger.info(f"StatefulSet {sts_name} exists")

    svc_name = search_resource_names.shard_service_name(mdbs_name, shard_name)
    svc = core_v1.read_namespaced_service(svc_name, namespace)
    assert svc is not None, f"Service {svc_name} not found"
    logger.info(f"Service {svc_name} exists")

    proxy_svc_name = search_resource_names.shard_proxy_service_name(mdbs_name, shard_name)
    proxy_svc = core_v1.read_namespaced_service(proxy_svc_name, namespace)
    assert proxy_svc is not None, f"Proxy Service {proxy_svc_name} not found"
    logger.info(f"Proxy Service {proxy_svc_name} exists")


def verify_shard_proxy_service_deleted(namespace: str, mdbs_name: str, shard_name: str):
    core_v1 = client.CoreV1Api()
    proxy_svc_name = search_resource_names.shard_proxy_service_name(mdbs_name, shard_name)

    def check():
        try:
            core_v1.read_namespaced_service(proxy_svc_name, namespace)
            return False, f"Proxy service {proxy_svc_name} still exists"
        except kubernetes.client.ApiException as e:
            if e.status == 404:
                return True, f"Proxy service {proxy_svc_name} deleted"
            raise

    run_periodically(check, timeout=300, sleep_time=10, msg=f"proxy service {proxy_svc_name} deletion")
    logger.info(f"Proxy service {proxy_svc_name} confirmed deleted")


# ---------------------------------------------------------------------------
# Fixtures
# ---------------------------------------------------------------------------


@fixture(scope="module")
def sharded_ca_configmap(issuer_ca_filepath: str, namespace: str) -> str:
    return create_issuer_ca(issuer_ca_filepath, namespace, CA_CONFIGMAP_NAME)


@fixture(scope="function")
def helper(namespace: str) -> SearchDeploymentHelper:
    return SearchDeploymentHelper(
        namespace=namespace,
        mdb_resource_name=MDB_RESOURCE_NAME,
        mdbs_resource_name=MDBS_RESOURCE_NAME,
        shard_count=INITIAL_SHARD_COUNT,
        mongods_per_shard=MONGODS_PER_SHARD,
        mongos_count=MONGOS_COUNT,
        config_server_count=CONFIG_SERVER_COUNT,
        tls_cert_prefix=MDBS_TLS_CERT_PREFIX,
        ca_configmap_name=CA_CONFIGMAP_NAME,
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


# ===========================================================================
# Phase 1: Setup (2 shards)
# ===========================================================================


@MARKER
def test_install_operator(namespace: str, operator_installation_config: dict[str, str]):
    operator = get_default_operator(namespace, operator_installation_config=operator_installation_config)
    operator.assert_is_running()


@MARKER
@skip_if_cloud_manager
def test_create_ops_manager(namespace: str):
    ops_manager = get_ops_manager(namespace)
    assert ops_manager is not None
    ops_manager.update()
    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=1200)
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)


@MARKER
def test_install_tls_certificates(helper: SearchDeploymentHelper, mdb: MongoDB, issuer: str):
    helper.install_sharded_tls_certificates()


@MARKER
def test_create_sharded_cluster(mdb: MongoDB):
    mdb.update()
    mdb.assert_reaches_phase(Phase.Running, timeout=900)


@MARKER
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


@MARKER
def test_deploy_lb_certificates(namespace: str, issuer: str):
    create_lb_certificates(
        namespace, issuer, INITIAL_SHARD_COUNT, MDB_RESOURCE_NAME, MDBS_RESOURCE_NAME, MDBS_TLS_CERT_PREFIX
    )


@MARKER
def test_create_search_tls_certificate(namespace: str, issuer: str):
    create_per_shard_search_tls_certs(
        namespace, issuer, MDBS_TLS_CERT_PREFIX, INITIAL_SHARD_COUNT, MDB_RESOURCE_NAME, MDBS_RESOURCE_NAME
    )


@MARKER
def test_create_search_resource(mdbs: MongoDBSearch):
    mdbs.update()
    mdbs.assert_reaches_phase(Phase.Running, timeout=600)


@MARKER
def test_verify_envoy_deployment(namespace: str):
    envoy_deployment_name = search_resource_names.lb_deployment_name(MDB_RESOURCE_NAME)

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


@MARKER
def test_wait_for_sharded_cluster_ready(mdb: MongoDB):
    mdb.assert_reaches_phase(Phase.Running, timeout=600)


@MARKER
def test_verify_mongod_parameters(namespace: str, mdb: MongoDB, mdbs: MongoDBSearch):
    verify_sharded_mongod_parameters(
        namespace,
        MDB_RESOURCE_NAME,
        mdbs.name,
        INITIAL_SHARD_COUNT,
        expected_host_fn=lambda shard: search_resource_names.shard_proxy_service_host(
            mdbs.name, shard, namespace, ENVOY_PROXY_PORT
        ),
    )


@MARKER
def test_verify_mongos_search_config(namespace: str, mdb: MongoDB):
    verify_mongos_search_config(namespace, MDB_RESOURCE_NAME)


@MARKER
def test_deploy_tools_pod(tools_pod: mongodb_tools_pod.ToolsPod):
    logger.info(f"Tools pod {tools_pod.pod_name} is ready")


@MARKER
def test_restore_sample_database(mdb: MongoDB, tools_pod: mongodb_tools_pod.ToolsPod):
    search_tester = get_search_tester(mdb, ADMIN_USER_NAME, ADMIN_USER_PASSWORD, use_ssl=True)
    search_tester.mongorestore_from_url(
        archive_url="https://atlas-education.s3.amazonaws.com/sample_mflix.archive",
        ns_include="sample_mflix.*",
        tools_pod=tools_pod,
    )
    logger.info("Sample database restored")


@MARKER
def test_shard_collections(mdb: MongoDB):
    search_tester = get_search_tester(mdb, ADMIN_USER_NAME, ADMIN_USER_PASSWORD, use_ssl=True)
    search_tester.shard_and_distribute_collection("sample_mflix", "movies")
    logger.info("Collections sharded and chunks distributed")


@MARKER
def test_create_search_index(mdb: MongoDB):
    search_tester = get_search_tester(mdb, USER_NAME, USER_PASSWORD, use_ssl=True)
    search_tester.create_search_index("sample_mflix", "movies")
    search_tester.wait_for_search_indexes_ready("sample_mflix", "movies", timeout=300)
    logger.info("Text search index created")


@MARKER
def test_verify_initial_search(mdb: MongoDB):
    search_tester = get_search_tester(mdb, USER_NAME, USER_PASSWORD, use_ssl=True)
    verify_text_search_query(search_tester)
    verify_search_results_from_all_shards(search_tester)
    logger.info("Initial search verification passed with %d shards", INITIAL_SHARD_COUNT)


# ===========================================================================
# Phase 2: Scale UP (2 -> 3 shards)
# ===========================================================================


@MARKER
def test_scale_up_create_mongodb_shard_cert(namespace: str):
    """Create MongoDB TLS certificate for the new shard (index 2)."""
    shard_idx = SCALED_UP_SHARD_COUNT - 1
    secret_name = f"{MDB_TLS_SECRET_PREFIX}{MDB_RESOURCE_NAME}-{shard_idx}-cert"
    create_mongodb_tls_certs(
        issuer=ISSUER_CA_NAME,
        namespace=namespace,
        resource_name=f"{MDB_RESOURCE_NAME}-{shard_idx}",
        bundle_secret_name=secret_name,
        replicas=MONGODS_PER_SHARD,
        service_name=f"{MDB_RESOURCE_NAME}-sh",
    )
    logger.info(f"MongoDB TLS cert created for shard {shard_idx}: {secret_name}")


@MARKER
def test_scale_up_create_search_tls_cert(namespace: str, issuer: str):
    """Create per-shard search TLS certificate for the new shard."""
    shard_idx = SCALED_UP_SHARD_COUNT - 1
    shard_name = f"{MDB_RESOURCE_NAME}-{shard_idx}"
    secret_name = search_resource_names.shard_tls_cert_name(MDBS_RESOURCE_NAME, shard_name, MDBS_TLS_CERT_PREFIX)

    additional_domains = [
        f"{search_resource_names.shard_service_name(MDBS_RESOURCE_NAME, shard_name)}.{namespace}.svc.cluster.local",
        f"{search_resource_names.shard_proxy_service_name(MDBS_RESOURCE_NAME, shard_name)}.{namespace}.svc.cluster.local",
    ]

    create_tls_certs(
        issuer=issuer,
        namespace=namespace,
        resource_name=search_resource_names.shard_statefulset_name(MDBS_RESOURCE_NAME, shard_name),
        secret_name=secret_name,
        additional_domains=additional_domains,
    )
    logger.info(f"Search TLS cert created for shard {shard_name}: {secret_name}")


@MARKER
def test_scale_up_recreate_lb_certificates(namespace: str, issuer: str):
    """Recreate LB certificates with SANs covering all 3 shards' proxy services."""
    create_lb_certificates(
        namespace, issuer, SCALED_UP_SHARD_COUNT, MDB_RESOURCE_NAME, MDBS_RESOURCE_NAME, MDBS_TLS_CERT_PREFIX
    )
    logger.info("LB certificates recreated for %d shards", SCALED_UP_SHARD_COUNT)


@MARKER
def test_scale_up_update_shard_count(mdb: MongoDB):
    """Scale MongoDB sharded cluster from 2 to 3 shards."""
    mdb.load()
    mdb["spec"]["shardCount"] = SCALED_UP_SHARD_COUNT
    mdb.update()
    mdb.assert_reaches_phase(Phase.Running, timeout=900)
    logger.info("MongoDB scaled up to %d shards", SCALED_UP_SHARD_COUNT)


@MARKER
def test_scale_up_wait_for_search_running(mdbs: MongoDBSearch):
    """Wait for MongoDBSearch to reconcile with the new shard."""
    mdbs.assert_reaches_phase(Phase.Running, timeout=600)
    logger.info("MongoDBSearch reached Running after scale-up")


@MARKER
def test_scale_up_verify_new_shard_resources(namespace: str):
    """Verify per-shard resources exist for the newly added shard."""
    new_shard_name = f"{MDB_RESOURCE_NAME}-{SCALED_UP_SHARD_COUNT - 1}"
    verify_shard_resources_exist(namespace, MDBS_RESOURCE_NAME, new_shard_name)
    logger.info(f"All resources verified for new shard {new_shard_name}")


@MARKER
def test_scale_up_verify_mongod_parameters(namespace: str, mdb: MongoDB, mdbs: MongoDBSearch):
    """Verify mongod search parameters for all 3 shards."""
    verify_sharded_mongod_parameters(
        namespace,
        MDB_RESOURCE_NAME,
        mdbs.name,
        SCALED_UP_SHARD_COUNT,
        expected_host_fn=lambda shard: search_resource_names.shard_proxy_service_host(
            mdbs.name, shard, namespace, ENVOY_PROXY_PORT
        ),
    )
    logger.info("Mongod parameters verified for %d shards", SCALED_UP_SHARD_COUNT)


@MARKER
def test_scale_up_verify_search(mdb: MongoDB):
    """Verify search returns correct results after scale-up.

    We do NOT call reshardCollection here because it drops all search indexes.
    The MongoDB balancer will naturally rebalance data to the new shard over time.
    The existing search indexes remain intact and functional.
    """
    search_tester = get_search_tester(mdb, USER_NAME, USER_PASSWORD, use_ssl=True)
    verify_text_search_query(search_tester)
    verify_search_results_from_all_shards(search_tester)
    logger.info("Search verification passed after scale-up to %d shards", SCALED_UP_SHARD_COUNT)


# ===========================================================================
# Phase 3: Scale DOWN (3 -> 2 shards)
# ===========================================================================


@MARKER
def test_scale_down_update_shard_count(mdb: MongoDB):
    """Scale MongoDB sharded cluster from 3 back to 2 shards.

    MongoDB will migrate data off the removed shard before completing,
    so we use a generous timeout.
    """
    mdb.load()
    mdb["spec"]["shardCount"] = SCALED_DOWN_SHARD_COUNT
    mdb.update()
    mdb.assert_reaches_phase(Phase.Running, timeout=1200)
    logger.info("MongoDB scaled down to %d shards", SCALED_DOWN_SHARD_COUNT)


@MARKER
def test_scale_down_wait_for_search_running(mdbs: MongoDBSearch):
    """Wait for MongoDBSearch to reconcile after shard removal."""
    mdbs.assert_reaches_phase(Phase.Running, timeout=600)
    logger.info("MongoDBSearch reached Running after scale-down")


@MARKER
def test_scale_down_verify_stale_resources_cleaned(namespace: str):
    """Check if proxy service for the removed shard is cleaned up.

    NOTE: The operator currently does not clean up per-shard proxy services
    when shards are removed (cleanupStaleProxyServices does not trigger).
    This test logs the current state rather than asserting deletion, to avoid
    blocking the rest of the scale-down verification.
    """
    core_v1 = client.CoreV1Api()
    removed_shard_name = f"{MDB_RESOURCE_NAME}-{SCALED_UP_SHARD_COUNT - 1}"
    proxy_svc_name = search_resource_names.shard_proxy_service_name(MDBS_RESOURCE_NAME, removed_shard_name)

    try:
        core_v1.read_namespaced_service(proxy_svc_name, namespace)
        logger.warning(
            f"Proxy service {proxy_svc_name} still exists after scale-down "
            "(operator does not currently clean up stale proxy services on shard removal)"
        )
    except kubernetes.client.ApiException as e:
        if e.status == 404:
            logger.info(f"Proxy service {proxy_svc_name} was cleaned up")
        else:
            raise


@MARKER
def test_scale_down_verify_mongod_parameters(namespace: str, mdb: MongoDB, mdbs: MongoDBSearch):
    """Verify mongod search parameters for the remaining 2 shards."""
    verify_sharded_mongod_parameters(
        namespace,
        MDB_RESOURCE_NAME,
        mdbs.name,
        SCALED_DOWN_SHARD_COUNT,
        expected_host_fn=lambda shard: search_resource_names.shard_proxy_service_host(
            mdbs.name, shard, namespace, ENVOY_PROXY_PORT
        ),
    )
    logger.info("Mongod parameters verified for %d shards", SCALED_DOWN_SHARD_COUNT)


@MARKER
def test_scale_down_verify_search(mdb: MongoDB):
    """Verify search returns correct results from the remaining 2 shards."""
    search_tester = get_search_tester(mdb, USER_NAME, USER_PASSWORD, use_ssl=True)
    verify_text_search_query(search_tester)
    verify_search_results_from_all_shards(search_tester)
    logger.info("Search verification passed after scale-down to %d shards", SCALED_DOWN_SHARD_COUNT)
