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
from tests.common.search.connectivity import wait_for_resource_deleted
from tests.common.search.search_deployment_helper import SearchDeploymentHelper
from tests.common.search.search_resource_names import (
    metrics_forwarder_configmap_name,
    metrics_forwarder_deployment_name,
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
    operator.wait_for_operator_ready()


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

    run_periodically(
        check_metrics_forwarder_status,
        timeout=120,
        sleep_time=10,
        msg="metrics forwarder status to reach Running",
    )

    # Per-cluster surface: single-cluster + managed LB + metrics forwarder enabled, so the
    # one status.clusters entry must report search, loadBalancer AND metricsForwarder Running.
    mdbs.assert_cluster_statuses(expected_count=1, expect_managed_lb=True, expect_metrics_forwarder=True)

    tester = om.get_om_tester(project_name=MDB_RESOURCE_NAME)
    tester.assert_mongot_hosts_converged(mdbs.mongot_pod_hostnames())


@mark.e2e_search_enterprise_metrics_forwarder_replicaset
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

    run_periodically(
        check_metrics_forwarder_status,
        timeout=120,
        sleep_time=10,
        msg="metrics forwarder status to reach Disabled",
    )

    # Verify resources are cleaned up
    def check_deployment_deleted():
        try:
            client.AppsV1Api().read_namespaced_deployment(
                metrics_forwarder_deployment_name(MDB_RESOURCE_NAME), mdbs.namespace
            )
            return False
        except ApiException as e:
            return e.status == 404

    run_periodically(
        check_deployment_deleted,
        timeout=60,
        sleep_time=5,
        msg="metrics forwarder Deployment cleanup",
    )

    def check_configmap_deleted():
        try:
            client.CoreV1Api().read_namespaced_config_map(
                metrics_forwarder_configmap_name(MDB_RESOURCE_NAME), mdbs.namespace
            )
            return False
        except ApiException as e:
            return e.status == 404

    run_periodically(
        check_configmap_deleted,
        timeout=60,
        sleep_time=5,
        msg="metrics forwarder ConfigMap cleanup",
    )

    # Per-cluster surface: with the forwarder disabled, the metricsForwarder sub-phase must
    # drop out of status.clusters (search + loadBalancer stay Running).
    def check_per_cluster_metrics_forwarder_absent():
        mdbs.reload()
        cs = mdbs.get_cluster_status(0)
        return cs is not None and not cs.get("metricsForwarder")

    run_periodically(
        check_per_cluster_metrics_forwarder_absent,
        timeout=120,
        sleep_time=10,
        msg="per-cluster metrics forwarder status cleanup",
    )
    mdbs.assert_cluster_statuses(expected_count=1, expect_managed_lb=True, expect_metrics_forwarder=False)


@mark.e2e_search_enterprise_metrics_forwarder_replicaset
def test_deleteing_search_resource_deletes_hosts(om: MongoDBOpsManager, mdbs: MongoDBSearch):
    ensure_nested_objects(mdbs, ["spec", "observability", "metricsForwarder"])
    mdbs["spec"]["observability"]["metricsForwarder"]["mode"] = "enabled"
    mdbs.update()
    mdbs.assert_reaches_phase(Phase.Running, timeout=300)

    def metrics_forwarder_running():
        mdbs.reload()
        status = mdbs.get_metrics_forwarder_status()
        return status is not None and status.get("phase") == Phase.Running.name

    run_periodically(
        metrics_forwarder_running,
        timeout=120,
        sleep_time=10,
        msg="metrics forwarder status to reconverge before Search deletion",
    )

    deployment_name = metrics_forwarder_deployment_name(MDB_RESOURCE_NAME)
    configmap_name = metrics_forwarder_configmap_name(MDB_RESOURCE_NAME)
    apps = client.AppsV1Api()
    core = client.CoreV1Api()
    apps.read_namespaced_deployment(deployment_name, mdbs.namespace)
    core.read_namespaced_config_map(configmap_name, mdbs.namespace)

    mdbs.delete()

    tester = om.get_om_tester(project_name=MDB_RESOURCE_NAME)
    tester.assert_mongot_hosts_converged(set())
    wait_for_resource_deleted(
        lambda: apps.read_namespaced_deployment(deployment_name, mdbs.namespace),
        f"metrics-forwarder Deployment {mdbs.namespace}/{deployment_name}",
    )
    wait_for_resource_deleted(
        lambda: core.read_namespaced_config_map(configmap_name, mdbs.namespace),
        f"metrics-forwarder ConfigMap {mdbs.namespace}/{configmap_name}",
    )
