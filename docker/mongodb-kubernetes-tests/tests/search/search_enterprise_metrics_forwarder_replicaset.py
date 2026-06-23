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
    mongot_service_name,
    mongot_statefulset_name,
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

MDB_RESOURCE_NAME = "mdb-rs"


@fixture(scope="function")
def helper(namespace: str) -> SearchDeploymentHelper:
    return SearchDeploymentHelper(
        namespace=namespace,
        mdb_resource_name=MDB_RESOURCE_NAME,
        mdbs_resource_name=MDB_RESOURCE_NAME,
    )


@fixture(scope="function")
def om(namespace: str) -> MongoDBOpsManager:
    ops_manager = get_ops_manager(namespace)
    assert ops_manager is not None
    return ops_manager


@fixture(scope="function")
def mdb(namespace: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("enterprise-replicaset-sample-mflix.yaml"),
        name=MDB_RESOURCE_NAME,
        namespace=namespace,
    )
    resource.configure(om=get_ops_manager(namespace), project_name=MDB_RESOURCE_NAME)
    try_load(resource)
    return resource


@fixture(scope="function")
def mdbs(namespace: str) -> MongoDBSearch:
    resource = MongoDBSearch.from_yaml(yaml_fixture("search-minimal.yaml"), namespace=namespace, name=MDB_RESOURCE_NAME)
    ensure_nested_objects(resource["spec"]["clusters"][0], ["loadBalancer", "managed"])
    resource["spec"]["clusters"][0]["replicas"] = 1
    try_load(resource)
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


@mark.e2e_search_enterprise_metrics_forwarder_replicaset
def test_install_operator(namespace: str, operator_installation_config: dict[str, str]):
    operator = get_default_operator(namespace, operator_installation_config=operator_installation_config)
    operator.assert_is_running()


@mark.e2e_search_enterprise_metrics_forwarder_replicaset
def test_create_ops_manager(om: MongoDBOpsManager):
    om.update()
    om.om_status().assert_reaches_phase(Phase.Running, timeout=1200)
    om.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_search_enterprise_metrics_forwarder_replicaset
def test_create_database_resource(mdb: MongoDB):
    mdb.update()
    mdb.assert_reaches_phase(Phase.Running, timeout=300)


@mark.e2e_search_enterprise_metrics_forwarder_replicaset
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


@mark.e2e_search_enterprise_metrics_forwarder_replicaset
def test_create_search_resource(mdbs: MongoDBSearch):
    mdbs.update()
    mdbs.assert_reaches_phase(Phase.Running, timeout=300)


@mark.e2e_search_enterprise_metrics_forwarder_replicaset
def test_metrics_forwarder_status(om: MongoDBOpsManager, mdbs: MongoDBSearch):
    def check_metrics_forwarder_status():
        mdbs.reload()
        status = mdbs.get_metrics_forwarder_status()
        return status is not None and status.get("phase") == Phase.Running.name

    run_periodically(check_metrics_forwarder_status, timeout=120, interval=10)

    assert_hosts(om, mdbs)


@mark.e2e_search_enterprise_metrics_forwarder_replicaset
def test_scaling_updates_hosts(om: MongoDBOpsManager, mdbs: MongoDBSearch):
    mdbs["spec"]["clusters"][0]["replicas"] = 3
    mdbs.update()
    mdbs.assert_reaches_phase(Phase.Running, timeout=300)
    assert_hosts(om, mdbs)

    mdbs["spec"]["clusters"][0]["replicas"] = 2
    mdbs.update()
    mdbs.assert_reaches_phase(Phase.Running, timeout=300)
    assert_hosts(om, mdbs)


@mark.e2e_search_enterprise_metrics_forwarder_replicaset
def test_disable_metrics_forwarder(mdbs: MongoDBSearch):
    """Disable the metrics forwarder and verify cleanup."""
    ensure_nested_objects(mdbs, ["spec", "observability", "metricsForwarder"])
    mdbs["spec"]["observability"]["metricsForwarder"]["mode"] = "disabled"
    mdbs.update()

    def check_metrics_forwarder_status():
        mdbs.update()
        status = mdbs.get_metrics_forwarder_status()
        return status is not None and status.get("phase") == Phase.Disabled.name

    run_periodically(check_metrics_forwarder_status, timeout=120, interval=10)

    # Verify resources are cleaned up
    def check_deployment_deleted():
        try:
            client.AppsV1Api().read_namespaced_deployment(
                metrics_forwarder_deployment_name(MDB_RESOURCE_NAME), mdbs.namespace
            )
            return False
        except ApiException as e:
            return e.status == 404

    run_periodically(check_deployment_deleted, timeout=60, interval=5)

    def check_configmap_deleted():
        try:
            client.CoreV1Api().read_namespaced_config_map(
                metrics_forwarder_configmap_name(MDB_RESOURCE_NAME), mdbs.namespace
            )
            return False
        except ApiException as e:
            return e.status == 404

    run_periodically(check_configmap_deleted, timeout=60, interval=5)


@mark.e2e_search_enterprise_metrics_forwarder_replicaset
def test_deleteing_search_resource_deletes_hosts(om: MongoDBOpsManager, mdbs: MongoDBSearch):
    mdbs.delete()

    mdbs["spec"]["clusters"][0]["replicas"] = 0
    assert_hosts(om, mdbs)


def assert_hosts(om: MongoDBOpsManager, mdbs: MongoDBSearch):
    """Assert Ops Manager has exactly one MONGOT host per mongot pod.

    Registering/deregistering MONGOT hosts in Ops Manager is an asynchronous step that can
    lag behind the MongoDBSearch resource reporting Running, so we poll until the registered
    hosts match the expected set exactly rather than asserting once. A count-only check can
    pass transiently while membership is still wrong (e.g. during a scale-down where a
    not-yet-deregistered host masks a missing one).
    """
    tester = om.get_om_tester(project_name=MDB_RESOURCE_NAME)

    replicas = mdbs["spec"]["clusters"][0].get("replicas", 1)
    expected_hostnames = {
        f"{mongot_statefulset_name(mdbs.name)}-{i}.{mongot_service_name(mdbs.name)}.{mdbs.namespace}.svc.cluster.local"
        for i in range(replicas)
    }

    hosts: list[dict] = []

    def mongot_hosts_converged():
        nonlocal hosts
        hosts = [h for h in tester.api_get_hosts()["results"] if h.get("typeName") == "MONGOT"]
        actual_hostnames = {h.get("hostname") for h in hosts}
        return actual_hostnames == expected_hostnames, f"expected {expected_hostnames}, got {actual_hostnames}"

    run_periodically(
        mongot_hosts_converged,
        timeout=120,
        sleep_time=10,
        msg="Ops Manager MONGOT hosts to converge to the expected set",
    )

    for host in hosts:
        assert host.get("port") == 27028, f"Expected port 27028, got {host.get('port')} for host {host.get('hostname')}"
