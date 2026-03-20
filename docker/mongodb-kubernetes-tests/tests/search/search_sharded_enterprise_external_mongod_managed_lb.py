"""
E2E test for sharded MongoDB Search with external MongoDB source and operator-managed Envoy LB.

This test is a hybrid of two existing tests:
- search_sharded_enterprise_external_mongod.py (external source, BYO Envoy)
- search_sharded_enterprise_managed_lb.py (internal source, managed LB)

It verifies:
- Deploys a sharded MongoDB cluster with TLS enabled (simulating an external cluster)
- Deploys MongoDBSearch with spec.source.external.shardedCluster + lb.mode: Managed
- Operator automatically deploys and configures Envoy proxy (no manual deploy_envoy_proxy)
- Verifies per-shard mongot Services and StatefulSets
- Verifies mongod/mongos search parameters point to operator-managed Envoy proxy
- Imports sample data, shards collections, creates search indexes
- Executes search queries through mongos and verifies results from all shards

Key difference from BYO LB external test:
- No deploy_envoy_proxy() call - the operator deploys Envoy via lb.mode: Managed
- We verify the operator-created ConfigMap, Deployment, and proxy Services

Key difference from managed LB internal test:
- Uses spec.source.external.shardedCluster (external MongoDB source)
- MDB and MDBS have different resource names (mdb-sh vs mdb-sh-search)
- MongoDB shardOverrides are configured upfront (pointing to operator-managed proxy services)
"""

import time

from kubernetes import client
from kubetester.certs import create_tls_certs
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
from tests.common.search.sharded_search_helper import *
from tests.conftest import get_default_operator
from tests.search.om_deployment import get_ops_manager

logger = test_logger.get_test_logger(__name__)

# OCP Route port (passthrough TLS routes listen on 443)
OCP_ROUTE_PORT = 443

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
MDB_RESOURCE_NAME = "mdb-sh"  # MongoDB resource
MDBS_RESOURCE_NAME = "mdb-sh-search"  # MongoDBSearch resource (different name since external)
SHARD_COUNT = 2
MONGODS_PER_SHARD = 1
MONGOS_COUNT = 1
CONFIG_SERVER_COUNT = 1

# TLS configuration
MDBS_TLS_CERT_PREFIX = "certs"
CA_CONFIGMAP_NAME = "mdb-sh-ca"

# Module-level storage for route hostnames (populated by test_create_ocp_routes)
_route_hostnames: dict[str, str] = {}


def create_ocp_routes_for_shards(namespace: str) -> dict[str, str]:
    """Create OCP passthrough Routes for each shard's proxy service.

    Returns a dict mapping shard_name -> route hostname (auto-assigned by OCP).
    """
    custom_api = client.CustomObjectsApi()
    hostnames = {}

    for shard_idx in range(SHARD_COUNT):
        shard_name = f"{MDB_RESOURCE_NAME}-{shard_idx}"
        route_name = f"mongot-{shard_name}"
        proxy_svc = search_resource_names.shard_proxy_service_name(MDBS_RESOURCE_NAME, shard_name)

        route_body = {
            "apiVersion": "route.openshift.io/v1",
            "kind": "Route",
            "metadata": {"name": route_name, "namespace": namespace},
            "spec": {
                "to": {"kind": "Service", "name": proxy_svc},
                "port": {"targetPort": ENVOY_PROXY_PORT},
                "tls": {"termination": "passthrough"},
            },
        }

        try:
            custom_api.create_namespaced_custom_object(
                group="route.openshift.io", version="v1",
                namespace=namespace, plural="routes", body=route_body,
            )
            logger.info(f"Created OCP Route: {route_name} -> {proxy_svc}:{ENVOY_PROXY_PORT}")
        except client.exceptions.ApiException as e:
            if e.status == 409:
                logger.info(f"OCP Route {route_name} already exists")
            else:
                raise

    # Wait for OCP to assign hostnames, then read them back
    time.sleep(5)

    for shard_idx in range(SHARD_COUNT):
        shard_name = f"{MDB_RESOURCE_NAME}-{shard_idx}"
        route_name = f"mongot-{shard_name}"

        route = custom_api.get_namespaced_custom_object(
            group="route.openshift.io", version="v1",
            namespace=namespace, plural="routes", name=route_name,
        )
        hostname = route["spec"]["host"]
        hostnames[shard_name] = hostname
        logger.info(f"Route {route_name} assigned hostname: {hostname}")

    return hostnames


def derive_lb_endpoint_template(route_hostnames: dict[str, str]) -> str:
    """Derive the spec.lb.endpoint template from actual route hostnames.

    Takes one hostname like 'mongot-mdb-sh-0-ns.apps.domain' and replaces the
    shard name with {shardName} placeholder to get 'mongot-{shardName}-ns.apps.domain:443'.
    """
    first_shard = f"{MDB_RESOURCE_NAME}-0"
    hostname = route_hostnames[first_shard]
    # Replace the concrete shard name with the template placeholder
    template = hostname.replace(first_shard, "{shardName}")
    return f"{template}:{OCP_ROUTE_PORT}"


def create_lb_certificates_with_route_sans(
    namespace: str,
    issuer: str,
    route_hostnames: dict[str, str],
):
    """Create LB certificates with route hostnames in SANs instead of proxy service FQDNs."""
    lb_server_cert_name = search_resource_names.lb_server_cert_name(MDBS_RESOURCE_NAME, MDBS_TLS_CERT_PREFIX)
    lb_client_cert_name = search_resource_names.lb_client_cert_name(MDBS_RESOURCE_NAME, MDBS_TLS_CERT_PREFIX)

    # SANs include route hostnames instead of proxy service FQDNs
    additional_domains = list(route_hostnames.values())

    create_tls_certs(
        issuer=issuer,
        namespace=namespace,
        resource_name=search_resource_names.lb_deployment_name(MDBS_RESOURCE_NAME),
        replicas=1,
        service_name=search_resource_names.lb_service_name(MDBS_RESOURCE_NAME),
        additional_domains=additional_domains,
        secret_name=lb_server_cert_name,
    )
    logger.info(f"LB server certificate created with route SANs: {lb_server_cert_name}")

    create_tls_certs(
        issuer=issuer,
        namespace=namespace,
        resource_name=f"{search_resource_names.lb_deployment_name(MDBS_RESOURCE_NAME)}-client",
        replicas=1,
        service_name=search_resource_names.lb_service_name(MDBS_RESOURCE_NAME),
        additional_domains=[f"*.{namespace}.svc.cluster.local"],
        secret_name=lb_client_cert_name,
    )
    logger.info(f"LB client certificate created: {lb_client_cert_name}")


@fixture(scope="module")
def sharded_ca_configmap(issuer_ca_filepath: str, namespace: str) -> str:
    return create_issuer_ca(issuer_ca_filepath, namespace, CA_CONFIGMAP_NAME)


@fixture(scope="function")
def helper(namespace: str) -> SearchDeploymentHelper:
    return SearchDeploymentHelper(
        namespace=namespace,
        mdb_resource_name=MDB_RESOURCE_NAME,
        mdbs_resource_name=MDBS_RESOURCE_NAME,
    )


@fixture(scope="function")
def mdb(namespace: str, sharded_ca_configmap: str, helper: SearchDeploymentHelper) -> MongoDB:
    def mongot_host_fn(shard: str) -> str:
        if _route_hostnames:
            return f"{_route_hostnames[shard]}:{OCP_ROUTE_PORT}"
        return search_resource_names.shard_proxy_service_host(MDBS_RESOURCE_NAME, shard, namespace, ENVOY_PROXY_PORT)

    return helper.create_sharded_mdb(
        mongot_host_fn=mongot_host_fn,
        set_tls_ca=True,
    )


@fixture(scope="function")
def mdbs(namespace: str, mdb: MongoDB, helper: SearchDeploymentHelper) -> MongoDBSearch:
    lb_endpoint = derive_lb_endpoint_template(_route_hostnames) if _route_hostnames else None
    resource = helper.mdbs_for_ext_sharded_source(
        mongot_user_name=MONGOT_USER_NAME,
        lb_mode="Managed",
        lb_endpoint=lb_endpoint,
        replicas=2,
    )
    resource["spec"]["resourceRequirements"] = {
        "requests": {"cpu": "1", "memory": "2Gi"},
    }
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


@mark.e2e_search_sharded_enterprise_external_mongod_managed_lb
def test_install_operator(namespace: str, operator_installation_config: dict[str, str]):
    """Test that the operator is installed and running."""
    operator = get_default_operator(namespace, operator_installation_config=operator_installation_config)
    operator.assert_is_running()


@mark.e2e_search_sharded_enterprise_external_mongod_managed_lb
@skip_if_cloud_manager
def test_create_ops_manager(namespace: str):
    """Test OpsManager deployment (skipped for Cloud Manager)."""
    ops_manager = get_ops_manager(namespace)
    assert ops_manager is not None
    ops_manager.update()
    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=1200)
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_search_sharded_enterprise_external_mongod_managed_lb
def test_create_ocp_routes(namespace: str):
    """Create OCP passthrough Routes for each shard's proxy service and read back hostnames."""
    global _route_hostnames
    _route_hostnames = create_ocp_routes_for_shards(namespace)
    logger.info(f"Route hostnames: {_route_hostnames}")
    lb_endpoint = derive_lb_endpoint_template(_route_hostnames)
    logger.info(f"Derived lb.endpoint template: {lb_endpoint}")


@mark.e2e_search_sharded_enterprise_external_mongod_managed_lb
def test_install_tls_certificates(helper: SearchDeploymentHelper, mdb: MongoDB, issuer: str):
    helper.install_sharded_tls_certificates()


@mark.e2e_search_sharded_enterprise_external_mongod_managed_lb
def test_create_sharded_cluster(mdb: MongoDB):
    """Test sharded cluster deployment."""
    mdb.update()
    mdb.assert_reaches_phase(Phase.Running, timeout=900)


@mark.e2e_search_sharded_enterprise_external_mongod_managed_lb
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


@mark.e2e_search_sharded_enterprise_external_mongod_managed_lb
def test_deploy_lb_certificates(namespace: str, issuer: str):
    """Create TLS certificates for the operator-managed load balancer with route hostnames in SANs."""
    create_lb_certificates_with_route_sans(namespace, issuer, _route_hostnames)


@mark.e2e_search_sharded_enterprise_external_mongod_managed_lb
def test_create_search_tls_certificate(namespace: str, issuer: str):
    create_per_shard_search_tls_certs(
        namespace, issuer, MDBS_TLS_CERT_PREFIX, SHARD_COUNT, MDB_RESOURCE_NAME, MDBS_RESOURCE_NAME
    )


@mark.e2e_search_sharded_enterprise_external_mongod_managed_lb
def test_create_search_resource(mdbs: MongoDBSearch):
    """Test MongoDBSearch resource deployment with external sharded source + managed LB."""
    mdbs.update()
    mdbs.assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_search_sharded_enterprise_external_mongod_managed_lb
def test_verify_envoy_deployment(namespace: str):
    envoy_deployment_name = search_resource_names.lb_deployment_name(MDBS_RESOURCE_NAME)

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
    logger.info(f"Envoy Deployment {envoy_deployment_name} is running")


@mark.e2e_search_sharded_enterprise_external_mongod_managed_lb
def test_wait_for_sharded_cluster_ready(mdb: MongoDB):
    """Wait for sharded cluster to be ready after Search CR deployment."""
    mdb.assert_reaches_phase(Phase.Running, timeout=600)


# TODO: We don't really need this, it can be removed if we have a way to figure out a logical time
# to wait for to get the mongod/mongos config properly generated.
@mark.e2e_search_sharded_enterprise_external_mongod_managed_lb
def test_verify_mongod_parameters_per_shard(namespace: str, mdb: MongoDB, mdbs: MongoDBSearch):
    verify_sharded_mongod_parameters(
        namespace,
        MDB_RESOURCE_NAME,
        mdbs.name,
        SHARD_COUNT,
        expected_host_fn=lambda shard: f"{_route_hostnames[shard]}:{OCP_ROUTE_PORT}",
    )


# TODO: We don't really need this, it can be removed if we have a way to figure out a logical time
# to wait for to get the mongod/mongos config properly generated.
@mark.e2e_search_sharded_enterprise_external_mongod_managed_lb
def test_verify_mongos_search_config(namespace: str, mdb: MongoDB):
    verify_mongos_search_config(namespace, MDB_RESOURCE_NAME)


@mark.e2e_search_sharded_enterprise_external_mongod_managed_lb
def test_deploy_tools_pod(tools_pod: mongodb_tools_pod.ToolsPod):
    """Deploy mongodb-tools pod for running queries."""
    logger.info(f"Tools pod {tools_pod.pod_name} is ready")


@mark.e2e_search_sharded_enterprise_external_mongod_managed_lb
def test_restore_sample_database(mdb: MongoDB, tools_pod: mongodb_tools_pod.ToolsPod):
    search_tester = get_search_tester(mdb, ADMIN_USER_NAME, ADMIN_USER_PASSWORD, use_ssl=True)
    search_tester.mongorestore_from_url(
        archive_url="https://atlas-education.s3.amazonaws.com/sample_mflix.archive",
        ns_include="sample_mflix.*",
        tools_pod=tools_pod,
    )


@mark.e2e_search_sharded_enterprise_external_mongod_managed_lb
def test_shard_collections(mdb: MongoDB):
    search_tester = get_search_tester(mdb, ADMIN_USER_NAME, ADMIN_USER_PASSWORD, use_ssl=True)
    search_tester.shard_and_distribute_collection("sample_mflix", "movies")


@mark.e2e_search_sharded_enterprise_external_mongod_managed_lb
def test_create_search_index(mdb: MongoDB):
    search_tester = get_search_tester(mdb, USER_NAME, USER_PASSWORD, use_ssl=True)
    search_tester.create_search_index("sample_mflix", "movies")
    search_tester.wait_for_search_indexes_ready("sample_mflix", "movies", timeout=300)


@mark.e2e_search_sharded_enterprise_external_mongod_managed_lb
def test_execute_text_search_query(mdb: MongoDB):
    search_tester = get_search_tester(mdb, USER_NAME, USER_PASSWORD, use_ssl=True)
    verify_text_search_query(search_tester)


@mark.e2e_search_sharded_enterprise_external_mongod_managed_lb
def test_verify_search_results_from_all_shards(mdb: MongoDB):
    search_tester = get_search_tester(mdb, USER_NAME, USER_PASSWORD, use_ssl=True)
    verify_search_results_from_all_shards(search_tester)


@mark.e2e_search_sharded_enterprise_external_mongod_managed_lb
def test_verify_search_resource_status(mdbs: MongoDBSearch):
    """Verify the MongoDBSearch resource is in Running phase with correct status."""
    mdbs.load()

    phase = mdbs.get_status_phase()
    assert phase == Phase.Running, f"MongoDBSearch phase is {phase}, expected Running"

    logger.info(f"MongoDBSearch {mdbs.name} is in Running phase")
