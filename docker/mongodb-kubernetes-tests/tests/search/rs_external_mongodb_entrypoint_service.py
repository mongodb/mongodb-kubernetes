"""
E2E test for the stable entrypoint Service in MongoDBSearch (External RS).

This test verifies that the operator creates a stable entrypoint Service whose
name and port never change, even when the user transitions between:
  - Single mongot (no LB): entrypoint selects mongot pods, targetPort=27028
  - Multiple mongots (managed LB): entrypoint selects Envoy pods, targetPort=27029
  - Back to single mongot: entrypoint reverts to mongot pods

The mongotHost configured on mongod always points at the entrypoint Service,
so the user never needs to reconfigure their database when scaling search.

Phases:
  0. Infrastructure setup (operator, OM, TLS certs, MongoDB RS, users)
  1. Single mongot, no LB — verify entrypoint targets mongot directly
  2. Scale to 2 mongots + managed LB — verify entrypoint flips to Envoy
  3. Scale back to 1 mongot, remove LB — verify entrypoint reverts
"""

from kubernetes import client as k8s_client
from kubetester import get_service
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
from tests.common.search.search_resource_names import (
    entrypoint_service_host,
    entrypoint_service_name,
)
from tests.common.search.sharded_search_helper import create_issuer_ca, verify_text_search_query
from tests.conftest import get_default_operator
from tests.search.om_deployment import get_ops_manager

logger = test_logger.get_test_logger(__name__)

# Resource names
MDB_RESOURCE_NAME = "mdb-rs-ep"
MDBS_RESOURCE_NAME = "mdb-rs-ep-search"
RS_MEMBERS = 3

# Credentials
ADMIN_USER_NAME = "mdb-admin-user"
ADMIN_USER_PASSWORD = "mdb-admin-user-pass"
MONGOT_USER_NAME = "search-sync-source"
MONGOT_USER_PASSWORD = "search-sync-source-user-password"
USER_NAME = "mdb-user"
USER_PASSWORD = "mdb-user-pass"

# Ports
MONGOT_GRPC_PORT = 27028
ENVOY_PROXY_PORT = 27029

# TLS
MDBS_TLS_CERT_PREFIX = "certs"
CA_CONFIGMAP_NAME = f"{MDB_RESOURCE_NAME}-ca"

# Module-level state for cross-test assertions (e.g., verifying ClusterIP stability)
_state = {}

TEST_MARKER = "e2e_search_rs_external_entrypoint_svc"


# ---------------------------------------------------------------------------
# Assertion helpers
# ---------------------------------------------------------------------------


def assert_entrypoint_service(
    namespace: str,
    expected_selector: dict,
    expected_target_port: int,
    expected_port: int = MONGOT_GRPC_PORT,
    expected_cluster_ip: str | None = None,
) -> k8s_client.V1Service:
    """Fetch the entrypoint Service and assert its properties."""
    svc_name = entrypoint_service_name(MDBS_RESOURCE_NAME)

    def check():
        svc = get_service(namespace, svc_name)
        if svc is None:
            return False, f"Service {svc_name} not found"

        errors = []

        if svc.spec.type != "ClusterIP":
            errors.append(f"type={svc.spec.type}, expected ClusterIP")

        if svc.spec.cluster_ip in (None, "", "None"):
            errors.append(f"cluster_ip={svc.spec.cluster_ip}, expected a real IP (not headless)")

        if svc.spec.selector != expected_selector:
            errors.append(f"selector={svc.spec.selector}, expected {expected_selector}")

        grpc_ports = [p for p in svc.spec.ports if p.port == expected_port]
        if not grpc_ports:
            errors.append(f"no port with port={expected_port} found, ports={[(p.port, p.target_port) for p in svc.spec.ports]}")
        elif grpc_ports[0].target_port != expected_target_port:
            errors.append(f"targetPort={grpc_ports[0].target_port}, expected {expected_target_port}")

        if expected_cluster_ip and svc.spec.cluster_ip != expected_cluster_ip:
            errors.append(f"cluster_ip={svc.spec.cluster_ip}, expected stable IP {expected_cluster_ip}")

        if errors:
            return False, "; ".join(errors)
        return True, f"Service {svc_name} OK"

    run_periodically(check, timeout=120, sleep_time=5, msg=f"Entrypoint Service {svc_name}")

    svc = get_service(namespace, svc_name)
    logger.info(f"Entrypoint Service {svc_name}: selector={svc.spec.selector}, clusterIP={svc.spec.cluster_ip}")
    return svc


def assert_envoy_deployment_ready(namespace: str):
    """Wait for the operator-managed Envoy Deployment to be ready."""
    envoy_name = search_resource_names.lb_deployment_name(MDBS_RESOURCE_NAME)

    def check():
        try:
            dep = k8s_client.AppsV1Api().read_namespaced_deployment(envoy_name, namespace)
            ready = dep.status.ready_replicas or 0
            return ready >= 1, f"ready_replicas={ready}"
        except Exception as e:
            return False, f"Deployment {envoy_name} not found: {e}"

    run_periodically(check, timeout=120, sleep_time=5, msg=f"Envoy Deployment {envoy_name}")
    logger.info(f"Envoy Deployment {envoy_name} is running")


def assert_envoy_deployment_gone(namespace: str):
    """Wait for the Envoy Deployment to be deleted (garbage collected)."""
    envoy_name = search_resource_names.lb_deployment_name(MDBS_RESOURCE_NAME)

    def check():
        try:
            k8s_client.AppsV1Api().read_namespaced_deployment(envoy_name, namespace)
            return False, f"Deployment {envoy_name} still exists"
        except k8s_client.ApiException as e:
            if e.status == 404:
                return True, "Deployment deleted"
            return False, f"Unexpected error: {e}"

    run_periodically(check, timeout=120, sleep_time=5, msg=f"Envoy Deployment {envoy_name} cleanup")
    logger.info(f"Envoy Deployment {envoy_name} is gone")


def assert_no_envoy_deployment(namespace: str):
    """Assert the Envoy Deployment does NOT exist (immediate check, no polling)."""
    envoy_name = search_resource_names.lb_deployment_name(MDBS_RESOURCE_NAME)
    try:
        k8s_client.AppsV1Api().read_namespaced_deployment(envoy_name, namespace)
        raise AssertionError(f"Envoy Deployment {envoy_name} should not exist")
    except k8s_client.ApiException as e:
        assert e.status == 404, f"Unexpected error checking Envoy Deployment: {e}"
    logger.info(f"Confirmed: no Envoy Deployment {envoy_name}")


# ---------------------------------------------------------------------------
# Fixtures
# ---------------------------------------------------------------------------


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
    """MongoDB RS pre-configured with mongotHost pointing to the stable entrypoint Service."""
    mongot_host = entrypoint_service_host(MDBS_RESOURCE_NAME, namespace, MONGOT_GRPC_PORT)
    return helper.create_rs_mdb(set_tls=True, mongot_host=mongot_host)


@fixture(scope="function")
def mdbs(namespace: str, helper: SearchDeploymentHelper) -> MongoDBSearch:
    """MongoDBSearch with external RS source, single replica, no LB (initial state)."""
    return helper.mdbs_for_ext_rs_source(
        mongot_user_name=MONGOT_USER_NAME,
        replicas=1,
    )


@fixture(scope="function")
def admin_user(helper: SearchDeploymentHelper) -> MongoDBUser:
    return helper.admin_user_resource(ADMIN_USER_NAME)


@fixture(scope="function")
def user(helper: SearchDeploymentHelper) -> MongoDBUser:
    return helper.user_resource(USER_NAME)


@fixture(scope="function")
def mongot_user(helper: SearchDeploymentHelper, mdbs: MongoDBSearch) -> MongoDBUser:
    return helper.mongot_user_resource(mdbs, MONGOT_USER_NAME)


# ---------------------------------------------------------------------------
# Phase 0: Infrastructure Setup
# ---------------------------------------------------------------------------


@mark.e2e_search_rs_external_entrypoint_svc
def test_install_operator(namespace: str, operator_installation_config: dict[str, str]):
    operator = get_default_operator(namespace, operator_installation_config=operator_installation_config)
    operator.assert_is_running()


@mark.e2e_search_rs_external_entrypoint_svc
@skip_if_cloud_manager
def test_create_ops_manager(namespace: str):
    ops_manager = get_ops_manager(namespace)
    ops_manager.update()
    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=1200)
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_search_rs_external_entrypoint_svc
def test_install_tls_certificates(namespace: str, helper: SearchDeploymentHelper, mdb: MongoDB, issuer: str):
    """Create all TLS certificates upfront (RS members, mongot, LB server, LB client).

    Both mongot and LB server certs include the entrypoint Service domain as a SAN,
    so that mongod can connect through the entrypoint regardless of which mode is active.
    """
    # RS member certificates
    helper.install_rs_tls_certificates(issuer, members=RS_MEMBERS)

    ep_svc_domain = f"{entrypoint_service_name(MDBS_RESOURCE_NAME)}.{namespace}.svc.cluster.local"

    # Mongot TLS certificate (with entrypoint SAN)
    create_rs_search_tls_cert(
        namespace, issuer, MDBS_RESOURCE_NAME, MDBS_TLS_CERT_PREFIX,
        extra_domains=[ep_svc_domain],
    )

    # LB server + client certificates (with entrypoint SAN on the server cert)
    create_rs_lb_certificates(
        namespace, issuer, MDBS_RESOURCE_NAME, MDBS_TLS_CERT_PREFIX,
        extra_domains=[ep_svc_domain],
    )


@mark.e2e_search_rs_external_entrypoint_svc
def test_create_database_resource(mdb: MongoDB):
    mdb.update()
    mdb.assert_reaches_phase(Phase.Running, timeout=300)


@mark.e2e_search_rs_external_entrypoint_svc
def test_create_users(
    helper: SearchDeploymentHelper,
    admin_user: MongoDBUser,
    user: MongoDBUser,
    mongot_user: MongoDBUser,
    mdb: MongoDB,
):
    helper.deploy_users(
        admin_user, ADMIN_USER_PASSWORD,
        user, USER_PASSWORD,
        mongot_user, MONGOT_USER_PASSWORD,
    )


# ---------------------------------------------------------------------------
# Phase 1: Single Mongot, No LB
# ---------------------------------------------------------------------------


@mark.e2e_search_rs_external_entrypoint_svc
def test_create_search_resource_single(mdbs: MongoDBSearch):
    """Deploy MongoDBSearch with 1 replica, no LB."""
    mdbs.update()
    mdbs.assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_search_rs_external_entrypoint_svc
def test_verify_entrypoint_service_single_mongot(namespace: str):
    """Core Phase 1 assertion: entrypoint Service targets mongot pods directly."""
    mongot_selector = {"app": search_resource_names.mongot_service_name(MDBS_RESOURCE_NAME)}
    svc = assert_entrypoint_service(
        namespace,
        expected_selector=mongot_selector,
        expected_target_port=MONGOT_GRPC_PORT,
    )
    _state["cluster_ip"] = svc.spec.cluster_ip
    logger.info(f"Phase 1: entrypoint ClusterIP = {_state['cluster_ip']}")


@mark.e2e_search_rs_external_entrypoint_svc
def test_verify_headless_service_exists(namespace: str):
    """The headless Service (StatefulSet governing service) must still exist alongside the entrypoint."""
    headless_svc_name = search_resource_names.mongot_service_name(MDBS_RESOURCE_NAME)
    svc = get_service(namespace, headless_svc_name)
    assert svc is not None, f"Headless Service {headless_svc_name} not found"
    assert svc.spec.cluster_ip == "None", f"Expected headless (clusterIP=None), got {svc.spec.cluster_ip}"
    logger.info(f"Headless Service {headless_svc_name} exists with clusterIP=None")


@mark.e2e_search_rs_external_entrypoint_svc
def test_verify_no_envoy_deployment(namespace: str):
    """Envoy Deployment should not exist when there is no LB."""
    assert_no_envoy_deployment(namespace)


@mark.e2e_search_rs_external_entrypoint_svc
def test_wait_for_database_ready_phase1(mdb: MongoDB):
    mdb.assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_search_rs_external_entrypoint_svc
def test_verify_mongod_parameters(namespace: str, mdb: MongoDB):
    """Verify mongod's mongotHost points at the entrypoint Service."""
    expected_host = entrypoint_service_host(MDBS_RESOURCE_NAME, namespace, MONGOT_GRPC_PORT)
    verify_rs_mongod_parameters(namespace, MDB_RESOURCE_NAME, RS_MEMBERS, expected_host)


@mark.e2e_search_rs_external_entrypoint_svc
def test_deploy_tools_pod(tools_pod: mongodb_tools_pod.ToolsPod):
    logger.info(f"Tools pod {tools_pod.pod_name} is ready")


@mark.e2e_search_rs_external_entrypoint_svc
def test_restore_sample_database(mdb: MongoDB, tools_pod: mongodb_tools_pod.ToolsPod):
    search_tester = get_rs_search_tester(mdb, ADMIN_USER_NAME, ADMIN_USER_PASSWORD, use_ssl=True)
    search_tester.mongorestore_from_url(
        archive_url="https://atlas-education.s3.amazonaws.com/sample_mflix.archive",
        ns_include="sample_mflix.*",
        tools_pod=tools_pod,
    )


@mark.e2e_search_rs_external_entrypoint_svc
def test_create_search_index(mdb: MongoDB):
    search_tester = get_rs_search_tester(mdb, USER_NAME, USER_PASSWORD, use_ssl=True)
    search_tester.create_search_index("sample_mflix", "movies")
    search_tester.wait_for_search_indexes_ready("sample_mflix", "movies", timeout=300)


@mark.e2e_search_rs_external_entrypoint_svc
def test_search_query_phase1(mdb: MongoDB):
    """Verify search works with single mongot through the entrypoint Service."""
    search_tester = get_rs_search_tester(mdb, USER_NAME, USER_PASSWORD, use_ssl=True)
    verify_text_search_query(search_tester)


# ---------------------------------------------------------------------------
# Phase 2: Scale Up to 2 Mongots + Managed LB
# ---------------------------------------------------------------------------


@mark.e2e_search_rs_external_entrypoint_svc
def test_scale_up_to_managed_lb(mdbs: MongoDBSearch):
    """Scale to 2 replicas and enable managed LB. mongotHost does NOT change."""
    mdbs.load()
    mdbs["spec"]["replicas"] = 2
    mdbs["spec"]["lb"] = {"mode": "Managed"}
    mdbs.update()
    mdbs.assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_search_rs_external_entrypoint_svc
def test_verify_envoy_deployment(namespace: str):
    """Envoy Deployment should now exist and be ready."""
    assert_envoy_deployment_ready(namespace)


@mark.e2e_search_rs_external_entrypoint_svc
def test_verify_entrypoint_service_managed_lb(namespace: str):
    """Core Phase 2 assertion: entrypoint Service flips to Envoy, but name/IP/port stay the same."""
    envoy_selector = {"app": search_resource_names.lb_deployment_name(MDBS_RESOURCE_NAME)}
    assert_entrypoint_service(
        namespace,
        expected_selector=envoy_selector,
        expected_target_port=ENVOY_PROXY_PORT,
        expected_cluster_ip=_state.get("cluster_ip"),
    )


@mark.e2e_search_rs_external_entrypoint_svc
def test_verify_mongod_parameters_unchanged(namespace: str, mdb: MongoDB):
    """mongotHost still points at the entrypoint — no reconfiguration needed."""
    expected_host = entrypoint_service_host(MDBS_RESOURCE_NAME, namespace, MONGOT_GRPC_PORT)
    verify_rs_mongod_parameters(namespace, MDB_RESOURCE_NAME, RS_MEMBERS, expected_host)


@mark.e2e_search_rs_external_entrypoint_svc
def test_search_query_phase2(mdb: MongoDB):
    """Search still works — traffic now flows through Envoy to 2 mongot pods."""
    search_tester = get_rs_search_tester(mdb, USER_NAME, USER_PASSWORD, use_ssl=True)
    verify_text_search_query(search_tester)


# ---------------------------------------------------------------------------
# Phase 3: Scale Back Down, Remove LB
# ---------------------------------------------------------------------------


@mark.e2e_search_rs_external_entrypoint_svc
def test_scale_down_remove_lb(mdbs: MongoDBSearch):
    """Scale back to 1 replica and remove LB. mongotHost still does NOT change."""
    mdbs.load()
    mdbs["spec"]["replicas"] = 1
    # Remove the lb section entirely
    if "lb" in mdbs["spec"]:
        mdbs["spec"]["lb"] = None
    mdbs.update()
    mdbs.assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_search_rs_external_entrypoint_svc
def test_verify_entrypoint_service_after_scaledown(namespace: str):
    """Core Phase 3 assertion: entrypoint Service reverts to mongot pods, same name/IP."""
    mongot_selector = {"app": search_resource_names.mongot_service_name(MDBS_RESOURCE_NAME)}
    assert_entrypoint_service(
        namespace,
        expected_selector=mongot_selector,
        expected_target_port=MONGOT_GRPC_PORT,
        expected_cluster_ip=_state.get("cluster_ip"),
    )


@mark.e2e_search_rs_external_entrypoint_svc
def test_verify_envoy_cleanup(namespace: str):
    """Envoy Deployment should be cleaned up (garbage collected via owner reference)."""
    assert_envoy_deployment_gone(namespace)


@mark.e2e_search_rs_external_entrypoint_svc
def test_search_query_phase3(mdb: MongoDB):
    """Search still works — back to direct single mongot through entrypoint."""
    search_tester = get_rs_search_tester(mdb, USER_NAME, USER_PASSWORD, use_ssl=True)
    verify_text_search_query(search_tester)


@mark.e2e_search_rs_external_entrypoint_svc
def test_verify_final_status(mdbs: MongoDBSearch):
    mdbs.load()
    phase = mdbs.get_status_phase()
    assert phase == Phase.Running, f"MongoDBSearch phase is {phase}, expected Running"
    logger.info(f"MongoDBSearch {mdbs.name} is in Running phase")
