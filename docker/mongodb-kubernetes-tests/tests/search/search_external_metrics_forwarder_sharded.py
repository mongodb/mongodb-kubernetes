"""
E2E test for the Ops Manager metrics forwarder against an EXTERNAL sharded MongoDB search source.

This mirrors search_enterprise_metrics_forwarder_sharded.py (internal mongodbResourceRef source) but
drives the forwarder from an external sharded source (spec.source.external.shardedCluster) fronted by
an operator-managed Envoy load balancer.

Key differences from the internal test:
- The MongoDB and MongoDBSearch resources use DIFFERENT names (external source pattern).
- The MongoDB is pre-configured with per-shard mongotHost overrides pointing to the managed Envoy
  proxy services.
- External sources can't auto-resolve the Ops Manager project, so the metrics forwarder is given an
  explicit spec.observability.metricsForwarder.opsManager block (project ConfigMap + agent
  credentials Secret) created by the operator for the source MongoDB.
- The set of shards is declared explicitly on the MongoDBSearch (the search controller derives the
  mongot deployments, and therefore the registered MONGOT hosts, from it). Scaling shards therefore
  updates BOTH the source MongoDB (shardCount + overrides) and the MongoDBSearch (source shard list).
  The operator rejects shardOverrides that reference shards beyond shardCount, so shardCount and the
  overrides are always moved together.

It verifies that the forwarder registers exactly one MONGOT host per mongot pod per shard in Ops
Manager, that scaling mongots and adding/removing shards register/deregister hosts, and that disabling
the forwarder cleans up.
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

# External source: the MongoDB and MongoDBSearch resources use different names.
MDB_RESOURCE_NAME = "mdb-sh"
MDBS_RESOURCE_NAME = "mdb-sh-search"

INITIAL_SHARD_COUNT = 2
SCALED_UP_SHARD_COUNT = 3
MONGODS_PER_SHARD = 1
MONGOS_COUNT = 1
CONFIG_SERVER_COUNT = 1

# The gRPC port reported to Ops Manager for every mongot host, and the port mongod uses to reach the
# managed Envoy proxy (MongotDefaultGrpcPort).
MONGOT_GRPC_PORT = 27028
ENVOY_PROXY_PORT = 27028

MDBS_TLS_CERT_PREFIX = "certs"
CA_CONFIGMAP_NAME = f"{MDB_RESOURCE_NAME}-ca"


def _mongot_set_parameter(host: str) -> dict:
    return {
        "mongotHost": host,
        "searchIndexManagementHostAndPort": host,
        "skipAuthenticationToSearchIndexManagementServer": False,
        "skipAuthenticationToMongot": False,
        "searchTLSMode": "requireTLS",
        "useGrpcForSearch": True,
    }


def _shard_proxy_host(shard_name: str, namespace: str) -> str:
    return search_resource_names.shard_proxy_service_host(MDBS_RESOURCE_NAME, shard_name, namespace, ENVOY_PROXY_PORT)


def _set_source_shard_count(mdb: MongoDB, namespace: str, shard_count: int) -> None:
    """Set the source MongoDB shardCount together with the per-shard mongotHost overrides (and mongos
    config). The operator rejects shardOverrides referencing shards beyond shardCount, so the two must
    always be updated in the same change."""
    mdb["spec"]["shardCount"] = shard_count
    overrides = []
    for shard_idx in range(shard_count):
        shard_name = f"{MDB_RESOURCE_NAME}-{shard_idx}"
        overrides.append(
            {
                "shardNames": [shard_name],
                "additionalMongodConfig": {
                    "setParameter": _mongot_set_parameter(_shard_proxy_host(shard_name, namespace))
                },
            }
        )
    mdb["spec"]["shardOverrides"] = overrides
    if "mongos" not in mdb["spec"]:
        mdb["spec"]["mongos"] = {}
    mdb["spec"]["mongos"]["additionalMongodConfig"] = {
        "setParameter": _mongot_set_parameter(_shard_proxy_host(f"{MDB_RESOURCE_NAME}-0", namespace))
    }


def _set_search_shard_count(mdbs: MongoDBSearch, namespace: str, shard_count: int) -> None:
    """Rebuild the external sharded source's explicit shard list on the MongoDBSearch. The search
    controller derives mongot deployments (and therefore the registered MONGOT hosts) from this list."""
    shards = []
    for shard_idx in range(shard_count):
        shard_name = f"{MDB_RESOURCE_NAME}-{shard_idx}"
        hosts = [
            f"{shard_name}-{m}.{MDB_RESOURCE_NAME}-sh.{namespace}.svc.cluster.local:27017"
            for m in range(MONGODS_PER_SHARD)
        ]
        shards.append({"shardName": shard_name, "hosts": hosts})
    mdbs["spec"]["source"]["external"]["shardedCluster"]["shards"] = shards


@fixture(scope="module")
def sharded_ca_configmap(issuer_ca_filepath: str, namespace: str) -> str:
    return create_issuer_ca(issuer_ca_filepath, namespace, CA_CONFIGMAP_NAME)


@fixture(scope="function")
def helper(namespace: str) -> SearchDeploymentHelper:
    # The source MongoDB and the MongoDBSearch start at INITIAL_SHARD_COUNT; TLS for the maximum
    # (scaled-up) shard count is provisioned up front so adding a shard later needs no cert bootstrap.
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
def om(namespace: str) -> MongoDBOpsManager:
    ops_manager = get_ops_manager(namespace)
    assert ops_manager is not None
    return ops_manager


@fixture(scope="function")
def mdb(namespace: str, sharded_ca_configmap: str, helper: SearchDeploymentHelper) -> MongoDB:
    # Per-shard mongotHost overrides are managed locally (per active shard count) rather than via
    # create_sharded_mdb, because the operator rejects shardOverrides beyond the current shardCount and
    # this test scales the shard count at runtime.
    resource = helper.create_sharded_mdb(set_tls_ca=True)
    if "shardOverrides" not in resource["spec"]:
        _set_source_shard_count(resource, namespace, INITIAL_SHARD_COUNT)
    return resource


@fixture(scope="function")
def mdbs(namespace: str, mdb: MongoDB, helper: SearchDeploymentHelper) -> MongoDBSearch:
    return helper.mdbs_for_ext_sharded_source(
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


@mark.e2e_search_external_metrics_forwarder_sharded
def test_install_operator(namespace: str, operator_installation_config: dict[str, str]):
    operator = get_default_operator(namespace, operator_installation_config=operator_installation_config)
    operator.wait_for_operator_ready()


@mark.e2e_search_external_metrics_forwarder_sharded
def test_create_ops_manager(om: MongoDBOpsManager):
    om.update()
    om.om_status().assert_reaches_phase(Phase.Running, timeout=1200)
    om.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_search_external_metrics_forwarder_sharded
def test_install_tls_certificates(helper: SearchDeploymentHelper, mdb: MongoDB, issuer: str):
    # Provision MongoDB server certs for the maximum shard count so the shard added later already has
    # its certificate.
    helper.install_sharded_tls_certificates(shard_count=SCALED_UP_SHARD_COUNT)


@mark.e2e_search_external_metrics_forwarder_sharded
def test_create_database_resource(mdb: MongoDB):
    mdb.update()
    mdb.assert_reaches_phase(Phase.Running, timeout=900)


@mark.e2e_search_external_metrics_forwarder_sharded
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


@mark.e2e_search_external_metrics_forwarder_sharded
def test_deploy_lb_certificates(namespace: str, issuer: str):
    # SANs cover every shard's proxy service up to SCALED_UP_SHARD_COUNT so the managed-LB certificate
    # does not need to be recreated when a shard is added.
    create_lb_certificates(
        namespace, issuer, SCALED_UP_SHARD_COUNT, MDB_RESOURCE_NAME, MDBS_RESOURCE_NAME, MDBS_TLS_CERT_PREFIX
    )


@mark.e2e_search_external_metrics_forwarder_sharded
def test_create_search_tls_certificate(namespace: str, issuer: str):
    # Per-shard search certs for every shard up to SCALED_UP_SHARD_COUNT so the shard added later
    # already has its certificate.
    create_per_shard_search_tls_certs(
        namespace, issuer, MDBS_TLS_CERT_PREFIX, SCALED_UP_SHARD_COUNT, MDB_RESOURCE_NAME, MDBS_RESOURCE_NAME
    )


@mark.e2e_search_external_metrics_forwarder_sharded
def test_create_search_resource(helper: SearchDeploymentHelper, mdb: MongoDB, mdbs: MongoDBSearch):
    # External sources can't auto-resolve the Ops Manager project; point the forwarder at the source
    # MongoDB's project ConfigMap and agent credentials. Requires mdb to already be Running.
    helper.configure_metrics_forwarder_opsmanager(mdbs, mdb)
    mdbs.update()
    mdbs.assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_search_external_metrics_forwarder_sharded
def test_metrics_forwarder_status(om: MongoDBOpsManager, mdbs: MongoDBSearch):
    def check_metrics_forwarder_status():
        mdbs.reload()
        status = mdbs.get_metrics_forwarder_status()
        return status is not None and status.get("phase") == Phase.Running.name

    run_periodically(check_metrics_forwarder_status, timeout=180, interval=10)

    tester = om.get_om_tester(project_name=MDB_RESOURCE_NAME)
    tester.assert_mongot_hosts_converged(
        mdbs.shard_mongot_pod_hostnames([f"{MDB_RESOURCE_NAME}-{i}" for i in range(INITIAL_SHARD_COUNT)]),
    )


@mark.e2e_search_external_metrics_forwarder_sharded
def test_scaling_shard_mongots_updates_hosts(om: MongoDBOpsManager, mdbs: MongoDBSearch):
    """Scaling a shard's mongot count up/down registers/deregisters one host per mongot pod per shard."""
    tester = om.get_om_tester(project_name=MDB_RESOURCE_NAME)

    mdbs["spec"]["clusters"][0]["replicas"] = 2
    mdbs.update()
    mdbs.assert_reaches_phase(Phase.Running, timeout=600)
    tester.assert_mongot_hosts_converged(
        mdbs.shard_mongot_pod_hostnames([f"{MDB_RESOURCE_NAME}-{i}" for i in range(INITIAL_SHARD_COUNT)]),
    )

    mdbs["spec"]["clusters"][0]["replicas"] = 1
    mdbs.update()
    mdbs.assert_reaches_phase(Phase.Running, timeout=600)
    tester.assert_mongot_hosts_converged(
        mdbs.shard_mongot_pod_hostnames([f"{MDB_RESOURCE_NAME}-{i}" for i in range(INITIAL_SHARD_COUNT)]),
    )


@mark.e2e_search_external_metrics_forwarder_sharded
def test_adding_shard_updates_hosts(om: MongoDBOpsManager, mdb: MongoDB, mdbs: MongoDBSearch, namespace: str):
    """Adding a shard registers a MONGOT host for the new shard's mongot pod(s).

    All TLS certificates for the scaled-up shard count were provisioned during setup. Add the shard to
    the source cluster first so the new shard's data source exists, then declare it on the search
    resource so its mongot can sync.
    """
    mdb.load()
    _set_source_shard_count(mdb, namespace, SCALED_UP_SHARD_COUNT)
    mdb.update()
    mdb.assert_reaches_phase(Phase.Running, timeout=900)

    mdbs.load()
    _set_search_shard_count(mdbs, namespace, SCALED_UP_SHARD_COUNT)
    mdbs.update()
    mdbs.assert_reaches_phase(Phase.Running, timeout=600)

    tester = om.get_om_tester(project_name=MDB_RESOURCE_NAME)
    tester.assert_mongot_hosts_converged(
        mdbs.shard_mongot_pod_hostnames([f"{MDB_RESOURCE_NAME}-{i}" for i in range(SCALED_UP_SHARD_COUNT)]),
    )


@mark.e2e_search_external_metrics_forwarder_sharded
def test_removing_shard_updates_hosts(om: MongoDBOpsManager, mdb: MongoDB, mdbs: MongoDBSearch, namespace: str):
    """Removing a shard deregisters the MONGOT host(s) of the removed shard.

    Remove the shard from the search resource first so its mongot is torn down before the data-bearing
    source shard disappears, then scale the source cluster down.
    """
    mdbs.load()
    _set_search_shard_count(mdbs, namespace, INITIAL_SHARD_COUNT)
    mdbs.update()
    mdbs.assert_reaches_phase(Phase.Running, timeout=600)

    mdb.load()
    _set_source_shard_count(mdb, namespace, INITIAL_SHARD_COUNT)
    mdb.update()
    mdb.assert_reaches_phase(Phase.Running, timeout=1200)

    tester = om.get_om_tester(project_name=MDB_RESOURCE_NAME)
    tester.assert_mongot_hosts_converged(
        mdbs.shard_mongot_pod_hostnames([f"{MDB_RESOURCE_NAME}-{i}" for i in range(INITIAL_SHARD_COUNT)]),
    )


@mark.e2e_search_external_metrics_forwarder_sharded
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

    # The forwarder Deployment/ConfigMap are named after the MongoDBSearch resource (not the source
    # MongoDB).
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
