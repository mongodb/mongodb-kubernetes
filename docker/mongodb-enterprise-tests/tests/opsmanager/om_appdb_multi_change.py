from kubetester import find_fixture
from kubetester.mongodb import Phase
from kubetester.opsmanager import MongoDBOpsManager
from pytest import fixture, mark
from tests.conftest import is_multi_cluster
from tests.opsmanager.withMonitoredAppDB.conftest import enable_multi_cluster_deployment


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
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_om_appdb_multi_change
def test_change_appdb(ops_manager: MongoDBOpsManager):
    """This change affects both the StatefulSet spec (agent flags) and the AutomationConfig (mongod config).
    Appdb controller is expected to perform wait after the automation config push so that all the pods got to not ready
    status and the next StatefulSet spec change didn't result in the immediate rolling upgrade.
     See CLOUDP-73296 for more details."""
    ops_manager.load()
    ops_manager["spec"]["applicationDatabase"]["agent"] = {"startupOptions": {"maxLogFiles": "30"}}
    ops_manager["spec"]["applicationDatabase"]["additionalMongodConfig"] = {"operationProfiling": {"mode": "slowOp"}}
    ops_manager.update()

    ops_manager.appdb_status().assert_abandons_phase(Phase.Running, timeout=100)
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=1200)


@mark.e2e_om_appdb_multi_change
def test_om_ok(ops_manager: MongoDBOpsManager):
    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=800)
    ops_manager.get_om_tester().assert_healthiness()
