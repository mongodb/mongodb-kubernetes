"""
E2E test for the Ops Manager metrics forwarder against an EXTERNAL ReplicaSet MongoDB search source.

This mirrors search_enterprise_metrics_forwarder_replicaset.py (internal mongodbResourceRef source)
but drives the forwarder from an external source (spec.source.external.hostAndPorts) fronted by an
operator-managed Envoy load balancer.

Key difference from the internal test:
- The MongoDB and MongoDBSearch resources use DIFFERENT names (external source pattern).
- The MongoDB is pre-configured with mongotHost pointing to the managed Envoy proxy service.
- External sources cannot auto-resolve the Ops Manager project, so the metrics forwarder is given an
  explicit spec.observability.metricsForwarder.opsManager block (project ConfigMap + agent credentials
  Secret) that the operator created for the source MongoDB. See
  SearchDeploymentHelper.configure_metrics_forwarder_opsmanager.

It verifies that the forwarder registers exactly one MONGOT host per mongot pod in Ops Manager, that
scaling the mongot replicas registers/deregisters hosts, and that disabling the forwarder cleans up.
"""

from kubernetes import client
from kubernetes.client.exceptions import ApiException
from kubetester.kubetester import ensure_nested_objects, run_periodically
from kubetester.mongodb import MongoDB
from kubetester.mongodb_search import MongoDBSearch
from kubetester.mongodb_user import MongoDBUser
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.phase import Phase
from pytest import fixture, mark
from tests import test_logger
from tests.common.search import search_resource_names
from tests.common.search.rs_search_helper import create_rs_lb_certificates, create_rs_search_tls_cert
from tests.common.search.search_deployment_helper import SearchDeploymentHelper
from tests.common.search.search_resource_names import (
    metrics_forwarder_configmap_name,
    metrics_forwarder_deployment_name,
)
from tests.common.search.sharded_search_helper import create_issuer_ca
from tests.conftest import get_default_operator
from tests.search.om_deployment import get_ops_manager

logger = test_logger.get_test_logger(__name__)

ADMIN_USER_NAME = "mdb-admin-user"
ADMIN_USER_PASSWORD = f"{ADMIN_USER_NAME}-password"

MONGOT_USER_NAME = "search-sync-source"
MONGOT_USER_PASSWORD = f"{MONGOT_USER_NAME}-password"

USER_NAME = "mdb-user"
USER_PASSWORD = f"{USER_NAME}-password"

# The gRPC port reported to Ops Manager for every mongot host, and the port mongod uses to reach the
# managed Envoy proxy (MongotDefaultGrpcPort).
MONGOT_GRPC_PORT = 27028
ENVOY_PROXY_PORT = 27028

# External source: the MongoDB and MongoDBSearch resources use different names.
MDB_RESOURCE_NAME = "mdb-rs"
MDBS_RESOURCE_NAME = "mdb-rs-search"
RS_MEMBERS = 3

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
def om(namespace: str) -> MongoDBOpsManager:
    ops_manager = get_ops_manager(namespace)
    assert ops_manager is not None
    return ops_manager


@fixture(scope="function")
def mdb(namespace: str, ca_configmap: str, helper: SearchDeploymentHelper) -> MongoDB:
    """MongoDB RS pre-configured with mongotHost pointing to the managed Envoy proxy (external source)."""
    proxy_host = search_resource_names.proxy_service_host(MDBS_RESOURCE_NAME, namespace, ENVOY_PROXY_PORT)
    return helper.create_rs_mdb(set_tls=True, mongot_host=proxy_host)


@fixture(scope="function")
def mdbs(namespace: str, mdb: MongoDB, helper: SearchDeploymentHelper) -> MongoDBSearch:
    return helper.mdbs_for_ext_rs_source(
        mongot_user_name=MONGOT_USER_NAME,
        lb_mode="Managed",
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
    return helper.mongot_user_resource(mdbs.name, MONGOT_USER_NAME)


@mark.e2e_search_external_metrics_forwarder_replicaset
def test_install_operator(namespace: str, operator_installation_config: dict[str, str]):
    operator = get_default_operator(namespace, operator_installation_config=operator_installation_config)
    operator.wait_for_operator_ready()


@mark.e2e_search_external_metrics_forwarder_replicaset
def test_create_ops_manager(om: MongoDBOpsManager):
    om.update()
    om.om_status().assert_reaches_phase(Phase.Running, timeout=1200)
    om.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_search_external_metrics_forwarder_replicaset
def test_install_tls_certificates(helper: SearchDeploymentHelper, mdb: MongoDB, issuer: str):
    helper.install_rs_tls_certificates(issuer, members=RS_MEMBERS)


@mark.e2e_search_external_metrics_forwarder_replicaset
def test_create_database_resource(mdb: MongoDB):
    mdb.update()
    mdb.assert_reaches_phase(Phase.Running, timeout=300)


@mark.e2e_search_external_metrics_forwarder_replicaset
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


@mark.e2e_search_external_metrics_forwarder_replicaset
def test_deploy_lb_certificates(namespace: str, issuer: str):
    create_rs_lb_certificates(namespace, issuer, MDBS_RESOURCE_NAME, MDBS_TLS_CERT_PREFIX)


@mark.e2e_search_external_metrics_forwarder_replicaset
def test_create_search_tls_certificate(namespace: str, issuer: str):
    create_rs_search_tls_cert(namespace, issuer, MDBS_RESOURCE_NAME, MDBS_TLS_CERT_PREFIX)


@mark.e2e_search_external_metrics_forwarder_replicaset
def test_create_search_resource(helper: SearchDeploymentHelper, mdb: MongoDB, mdbs: MongoDBSearch):
    # External sources can't auto-resolve the Ops Manager project; point the forwarder at the source
    # MongoDB's project ConfigMap and agent credentials. Requires mdb to already be Running.
    helper.configure_metrics_forwarder_opsmanager(mdbs, mdb)
    mdbs.update()
    mdbs.assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_search_external_metrics_forwarder_replicaset
def test_metrics_forwarder_status(om: MongoDBOpsManager, mdbs: MongoDBSearch):
    def check_metrics_forwarder_status():
        mdbs.reload()
        status = mdbs.get_metrics_forwarder_status()
        return status is not None and status.get("phase") == Phase.Running.name

    run_periodically(check_metrics_forwarder_status, timeout=120, interval=10)

    tester = om.get_om_tester(project_name=MDB_RESOURCE_NAME)
    tester.assert_mongot_hosts_converged(mdbs.mongot_pod_hostnames())


@mark.e2e_search_external_metrics_forwarder_replicaset
def test_scaling_updates_hosts(om: MongoDBOpsManager, mdbs: MongoDBSearch):
    tester = om.get_om_tester(project_name=MDB_RESOURCE_NAME)

    mdbs["spec"]["clusters"][0]["replicas"] = 3
    mdbs.update()
    mdbs.assert_reaches_phase(Phase.Running, timeout=300)
    tester.assert_mongot_hosts_converged(mdbs.mongot_pod_hostnames())

    mdbs["spec"]["clusters"][0]["replicas"] = 2
    mdbs.update()
    mdbs.assert_reaches_phase(Phase.Running, timeout=300)
    tester.assert_mongot_hosts_converged(mdbs.mongot_pod_hostnames())


@mark.e2e_search_external_metrics_forwarder_replicaset
def test_disable_metrics_forwarder(mdbs: MongoDBSearch):
    """Disable the metrics forwarder and verify cleanup."""
    ensure_nested_objects(mdbs, ["spec", "observability", "metricsForwarder"])
    mdbs["spec"]["observability"]["metricsForwarder"]["mode"] = "disabled"
    mdbs.update()

    def check_metrics_forwarder_status():
        mdbs.reload()
        status = mdbs.get_metrics_forwarder_status()
        return status is not None and status.get("phase") == Phase.Disabled.name

    run_periodically(check_metrics_forwarder_status, timeout=120, interval=10)

    # Verify resources are cleaned up. The forwarder Deployment/ConfigMap are named after the
    # MongoDBSearch resource (not the source MongoDB).
    def check_deployment_deleted():
        try:
            client.AppsV1Api().read_namespaced_deployment(
                metrics_forwarder_deployment_name(MDBS_RESOURCE_NAME), mdbs.namespace
            )
            return False
        except ApiException as e:
            return e.status == 404

    run_periodically(check_deployment_deleted, timeout=60, interval=5)

    def check_configmap_deleted():
        try:
            client.CoreV1Api().read_namespaced_config_map(
                metrics_forwarder_configmap_name(MDBS_RESOURCE_NAME), mdbs.namespace
            )
            return False
        except ApiException as e:
            return e.status == 404

    run_periodically(check_configmap_deleted, timeout=60, interval=5)
