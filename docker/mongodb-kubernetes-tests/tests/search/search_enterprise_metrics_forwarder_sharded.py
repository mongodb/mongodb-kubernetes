from kubernetes import client
from kubernetes.client.exceptions import ApiException
from kubetester import try_load
from kubetester.kubetester import ensure_nested_objects
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.kubetester import run_periodically
from kubetester.mongodb import MongoDB
from kubetester.mongodb_search import MongoDBSearch
from kubetester.mongodb_user import MongoDBUser
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.phase import Phase
from pytest import fixture, mark
from tests import test_logger
from tests.common.search.search_deployment_helper import SearchDeploymentHelper
from tests.common.search.search_resource_names import (
    metrics_forwarder_configmap_name,
    metrics_forwarder_deployment_name,
)
from tests.common.search.sharded_search_helper import (
    create_issuer_ca,
    create_lb_certificates,
    create_per_shard_search_tls_certs,
)
from tests.conftest import get_default_operator
from tests.search.om_deployment import get_ops_manager

logger = test_logger.get_test_logger(__name__)

ADMIN_USER_NAME = "mdb-admin-user"
ADMIN_USER_PASSWORD = f"{ADMIN_USER_NAME}-password"

MONGOT_USER_NAME = "search-sync-source"
MONGOT_USER_PASSWORD = f"{MONGOT_USER_NAME}-password"

USER_NAME = "mdb-user"
USER_PASSWORD = f"{USER_NAME}-password"

MDB_RESOURCE_NAME = "mdb-sh"
MDBS_RESOURCE_NAME = MDB_RESOURCE_NAME

INITIAL_SHARD_COUNT = 2
SCALED_UP_SHARD_COUNT = 3
MONGODS_PER_SHARD = 1
MONGOS_COUNT = 1
CONFIG_SERVER_COUNT = 1

# The gRPC port reported to Ops Manager for every mongot host (MongotDefaultGrpcPort).
MONGOT_GRPC_PORT = 27028

MDBS_TLS_CERT_PREFIX = "certs"
CA_CONFIGMAP_NAME = f"{MDB_RESOURCE_NAME}-ca"


@fixture(scope="module")
def sharded_ca_configmap(issuer_ca_filepath: str, namespace: str) -> str:
    return create_issuer_ca(issuer_ca_filepath, namespace, CA_CONFIGMAP_NAME)


@fixture(scope="function")
def helper(namespace: str) -> SearchDeploymentHelper:
    # TLS certificates are provisioned up front for the maximum (scaled-up) shard count so that
    # adding a shard later requires no certificate bootstrap. The cluster still starts at
    # INITIAL_SHARD_COUNT shards (from the YAML fixture) and is scaled up at runtime.
    return SearchDeploymentHelper(
        namespace=namespace,
        mdb_resource_name=MDB_RESOURCE_NAME,
        mdbs_resource_name=MDBS_RESOURCE_NAME,
        shard_count=SCALED_UP_SHARD_COUNT,
        mongods_per_shard=MONGODS_PER_SHARD,
        mongos_count=MONGOS_COUNT,
        config_server_count=CONFIG_SERVER_COUNT,
        tls_cert_prefix=MDBS_TLS_CERT_PREFIX,
        ca_configmap_name=CA_CONFIGMAP_NAME,
    )


@fixture(scope="function")
def om(namespace: str) -> MongoDBOpsManager:
    ops_manager = get_ops_manager(namespace)
    assert ops_manager is not None
    return ops_manager


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
    resource["spec"]["clusters"][0]["replicas"] = 1
    return resource


@fixture(scope="function")
def admin_user(helper: SearchDeploymentHelper) -> MongoDBUser:
    return helper.admin_user_resource(ADMIN_USER_NAME)


@fixture(scope="function")
def user(helper: SearchDeploymentHelper) -> MongoDBUser:
    return helper.user_resource(USER_NAME)


@fixture(scope="function")
def mongot_user(helper: SearchDeploymentHelper, mdbs: MongoDBSearch) -> MongoDBUser:
    return helper.mongot_user_resource(mdbs.name, MONGOT_USER_NAME)


@mark.e2e_search_enterprise_metrics_forwarder_sharded
def test_install_operator(namespace: str, operator_installation_config: dict[str, str]):
    operator = get_default_operator(namespace, operator_installation_config=operator_installation_config)
    operator.wait_for_operator_ready()


@mark.e2e_search_enterprise_metrics_forwarder_sharded
def test_create_ops_manager(om: MongoDBOpsManager):
    om.update()
    om.om_status().assert_reaches_phase(Phase.Running, timeout=1200)
    om.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_search_enterprise_metrics_forwarder_sharded
def test_install_tls_certificates(helper: SearchDeploymentHelper, mdb: MongoDB, issuer: str):
    # Creates MongoDB server certs for every shard up to the helper's shard_count
    # (SCALED_UP_SHARD_COUNT), so the shard added later already has its certificate.
    helper.install_sharded_tls_certificates()


@mark.e2e_search_enterprise_metrics_forwarder_sharded
def test_create_database_resource(mdb: MongoDB):
    mdb.update()
    mdb.assert_reaches_phase(Phase.Running, timeout=900)


@mark.e2e_search_enterprise_metrics_forwarder_sharded
def test_create_users(
    helper: SearchDeploymentHelper, admin_user: MongoDBUser, user: MongoDBUser, mongot_user: MongoDBUser
):
    helper.deploy_users(
        admin_user,
        ADMIN_USER_PASSWORD,
        user,
        USER_PASSWORD,
        mongot_user,
        MONGOT_USER_PASSWORD,
    )


@mark.e2e_search_enterprise_metrics_forwarder_sharded
def test_deploy_lb_certificates(namespace: str, issuer: str):
    # SANs cover every shard's proxy service up to SCALED_UP_SHARD_COUNT so the managed-LB
    # certificate does not need to be recreated when a shard is added.
    create_lb_certificates(
        namespace, issuer, SCALED_UP_SHARD_COUNT, MDB_RESOURCE_NAME, MDBS_RESOURCE_NAME, MDBS_TLS_CERT_PREFIX
    )


@mark.e2e_search_enterprise_metrics_forwarder_sharded
def test_create_search_tls_certificate(namespace: str, issuer: str):
    # Per-shard search certs are created for every shard up to SCALED_UP_SHARD_COUNT so the
    # shard added later already has its certificate.
    create_per_shard_search_tls_certs(
        namespace, issuer, MDBS_TLS_CERT_PREFIX, SCALED_UP_SHARD_COUNT, MDB_RESOURCE_NAME, MDBS_RESOURCE_NAME
    )


@mark.e2e_search_enterprise_metrics_forwarder_sharded
def test_create_search_resource(mdbs: MongoDBSearch):
    mdbs.update()
    mdbs.assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_search_enterprise_metrics_forwarder_sharded
def test_metrics_forwarder_status(om: MongoDBOpsManager, mdbs: MongoDBSearch):
    def check_metrics_forwarder_status():
        mdbs.reload()
        status = mdbs.get_metrics_forwarder_status()
        return status is not None and status.get("phase") == Phase.Running.name

    run_periodically(check_metrics_forwarder_status, timeout=180, interval=10)

    tester = om.get_om_tester(MDB_RESOURCE_NAME)
    tester.assert_mongot_hosts_converged(
        mdbs.shard_mongot_pod_hostnames([f"{mdbs.name}-{i}" for i in range(INITIAL_SHARD_COUNT)]),
    )


@mark.e2e_search_enterprise_metrics_forwarder_sharded
def test_scaling_shard_mongots_updates_hosts(om: MongoDBOpsManager, mdbs: MongoDBSearch):
    """Scaling a shard's mongot count up/down registers/deregisters one host per mongot pod per shard."""
    tester = om.get_om_tester(MDB_RESOURCE_NAME)

    mdbs["spec"]["clusters"][0]["replicas"] = 2
    mdbs.update()
    mdbs.assert_reaches_phase(Phase.Running, timeout=600)
    tester.assert_mongot_hosts_converged(
        mdbs.shard_mongot_pod_hostnames([f"{mdbs.name}-{i}" for i in range(INITIAL_SHARD_COUNT)]),
    )

    mdbs["spec"]["clusters"][0]["replicas"] = 1
    mdbs.update()
    mdbs.assert_reaches_phase(Phase.Running, timeout=600)
    tester.assert_mongot_hosts_converged(
        mdbs.shard_mongot_pod_hostnames([f"{mdbs.name}-{i}" for i in range(INITIAL_SHARD_COUNT)]),
    )


@mark.e2e_search_enterprise_metrics_forwarder_sharded
def test_adding_shard_updates_hosts(om: MongoDBOpsManager, mdb: MongoDB, mdbs: MongoDBSearch):
    """Adding a shard registers a MONGOT host for the new shard's mongot pod(s).

    All TLS certificates for the scaled-up shard count were provisioned during setup, so this
    only needs to bump the shard count.
    """
    mdb.load()
    mdb["spec"]["shardCount"] = SCALED_UP_SHARD_COUNT
    mdb.update()
    mdb.assert_reaches_phase(Phase.Running, timeout=900)

    mdbs.assert_reaches_phase(Phase.Running, timeout=600)
    tester = om.get_om_tester(MDB_RESOURCE_NAME)
    tester.assert_mongot_hosts_converged(
        mdbs.shard_mongot_pod_hostnames([f"{mdbs.name}-{i}" for i in range(SCALED_UP_SHARD_COUNT)]),
    )


@mark.e2e_search_enterprise_metrics_forwarder_sharded
def test_removing_shard_updates_hosts(om: MongoDBOpsManager, mdb: MongoDB, mdbs: MongoDBSearch):
    """Removing a shard deregisters the MONGOT host(s) of the removed shard."""
    mdb.load()
    mdb["spec"]["shardCount"] = INITIAL_SHARD_COUNT
    mdb.update()
    mdb.assert_reaches_phase(Phase.Running, timeout=1200)

    mdbs.assert_reaches_phase(Phase.Running, timeout=600)
    tester = om.get_om_tester(MDB_RESOURCE_NAME)
    tester.assert_mongot_hosts_converged(
        mdbs.shard_mongot_pod_hostnames([f"{mdbs.name}-{i}" for i in range(INITIAL_SHARD_COUNT)]),
    )


@mark.e2e_search_enterprise_metrics_forwarder_sharded
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
