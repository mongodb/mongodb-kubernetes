from kubetester import find_fixture, get_statefulset, scale_statefulset, wait_for_statefulset_replicas
from kubetester.kubetester import skip_if_multi_cluster
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.phase import Phase
from pytest import fixture, mark
from tests.conftest import is_multi_cluster
from tests.opsmanager.withMonitoredAppDB.conftest import enable_multi_cluster_deployment


def _assert_watch_reverts_scaling(namespace: str, sts_name: str, api_client):
    """Single-cluster StatefulSet watch assertion.

    The watch reacts to StatefulSet status updates, resolved through the controller ownerReference.
    Scaling the StatefulSet up directly drops readiness below the desired
    count, which only the operator reverts (Kubernetes never undoes a manual scale). A Running
    resource requeues only after 24h, so a prompt revert proves the watch drove the reconcile rather
    than the periodic resync."""
    sts = get_statefulset(namespace, sts_name, api_client=api_client)
    owner_refs = sts.metadata.owner_references or []
    assert any(ref.kind == "MongoDBOpsManager" for ref in owner_refs), (
        f"StatefulSet {sts_name} must be resolvable by the watch via an ownerReference to its "
        f"MongoDBOpsManager CR, but had ownerReferences={owner_refs}"
    )

    desired_replicas = sts.spec.replicas
    scale_statefulset(namespace, sts_name, desired_replicas + 1, api_client=api_client)
    wait_for_statefulset_replicas(namespace, sts_name, desired_replicas, api_client=api_client)


@fixture(scope="module")
def ops_manager(namespace: str, custom_version: str, custom_appdb_version: str) -> MongoDBOpsManager:
    resource = MongoDBOpsManager.from_yaml(find_fixture("om_ops_manager_basic.yaml"), namespace=namespace)

    resource.set_version(custom_version)
    resource.set_appdb_version(custom_appdb_version)

    if is_multi_cluster():
        enable_multi_cluster_deployment(resource)

    resource.update()
    return resource


@mark.e2e_om_appdb_multi_change
def test_appdb(ops_manager: MongoDBOpsManager):
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=800)


@mark.e2e_om_appdb_multi_change
def test_change_appdb(ops_manager: MongoDBOpsManager):
    """This change affects both the StatefulSet spec (agent flags) and the AutomationConfig (mongod config).
    Appdb controller is expected to perform wait after the automation config push so that all the pods got to not ready
    status and the next StatefulSet spec change didn't result in the immediate rolling upgrade.
     See CLOUDP-73296 for more details."""
    ops_manager.load()
    ops_manager["spec"]["applicationDatabase"]["agent"] = {"startupOptions": {"maxLogFiles": "30"}}
    ops_manager["spec"]["applicationDatabase"]["additionalMongodConfig"] = {
        "replication": {"enableMajorityReadConcern": "true"}
    }
    ops_manager.update()

    ops_manager.appdb_status().assert_abandons_phase(Phase.Running, timeout=100)
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=1200)


@mark.e2e_om_appdb_multi_change
def test_om_ok(ops_manager: MongoDBOpsManager):
    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=800)
    ops_manager.get_om_tester().assert_healthiness()


@mark.e2e_om_appdb_multi_change
@skip_if_multi_cluster()
def test_om_statefulset_watch_reverts_manual_scaling(ops_manager: MongoDBOpsManager, namespace: str):
    sts = ops_manager.read_statefulset()
    _assert_watch_reverts_scaling(namespace, sts.metadata.name, api_client=None)
    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=800)


@mark.e2e_om_appdb_multi_change
@skip_if_multi_cluster()
def test_appdb_statefulset_watch_reverts_manual_scaling(ops_manager: MongoDBOpsManager, namespace: str):
    sts = ops_manager.read_appdb_statefulset()
    _assert_watch_reverts_scaling(namespace, sts.metadata.name, api_client=None)
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)
