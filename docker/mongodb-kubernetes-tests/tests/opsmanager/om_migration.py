from typing import Optional

from kubetester import try_load
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.phase import Phase
from pytest import fixture, mark
from tests.conftest import is_multi_cluster
from tests.opsmanager.withMonitoredAppDB.conftest import enable_multi_cluster_deployment


@fixture(scope="module")
def ops_manager(namespace: str, custom_version: Optional[str], custom_appdb_version: str) -> MongoDBOpsManager:
    """The fixture for Ops Manager to be created."""
    om = MongoDBOpsManager.from_yaml(yaml_fixture("om_ops_manager_basic.yaml"), namespace=namespace)
    om.set_version(custom_version)
    om.set_appdb_version(custom_appdb_version)

    if is_multi_cluster():
        enable_multi_cluster_deployment(om)

    try_load(om)
    return om


@mark.e2e_om_migration
class TestOpsManagerOmMigration:
    def test_om_created(self, ops_manager: MongoDBOpsManager):
        ops_manager.update()
        # Backup is not fully configured so we wait until Pending phase
        ops_manager.appdb_status().assert_reaches_phase(Phase.Running)
        ops_manager.om_status().assert_reaches_phase(Phase.Running)

    def test_migrate_architecture(self, ops_manager: MongoDBOpsManager):
        ops_manager.trigger_architecture_migration()
        ops_manager.appdb_status().assert_abandons_phase(Phase.Running, timeout=1000)
        ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=1000)
