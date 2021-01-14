from typing import Optional

import pytest
from pytest import fixture

from kubetester.kubetester import (
    skip_if_local,
    fixture as yaml_fixture,
)
from kubetester.mongodb import Phase
from kubetester.opsmanager import MongoDBOpsManager

gen_key_resource_version = None
admin_key_resource_version = None
INITIAL_APPDB_VERSION = "4.0.20"


@fixture(scope="module")
def ops_manager(namespace: str, custom_version: Optional[str]) -> MongoDBOpsManager:
    resource: MongoDBOpsManager = MongoDBOpsManager.from_yaml(
        yaml_fixture("om_appdb_upgrade.yaml"), namespace=namespace
    )
    resource.set_version(custom_version)

    return resource.create()


@pytest.mark.e2e_om_appdb_upgrade
class TestOpsManagerCreation:
    """
    Creates an Ops Manager instance with AppDB of size 3. The test waits until the AppDB is ready, not the OM resource
    """

    def test_appdb(self, ops_manager: MongoDBOpsManager):
        ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)

        assert ops_manager.appdb_status().get_members() == 3
        assert ops_manager.appdb_status().get_version() == INITIAL_APPDB_VERSION
        db_pods = ops_manager.read_appdb_pods()
        for pod in db_pods:
            # the appdb pods by default have 500M
            assert pod.spec.containers[0].resources.requests["memory"] == "500M"

    def test_admin_config_map(self, ops_manager: MongoDBOpsManager):
        ops_manager.get_automation_config_tester().reached_version(1)

    @skip_if_local
    def test_mongod(self, ops_manager: MongoDBOpsManager):
        mdb_tester = ops_manager.get_appdb_tester()
        mdb_tester.assert_connectivity()
        mdb_tester.assert_version(INITIAL_APPDB_VERSION)

    def test_appdb_automation_config(self, ops_manager: MongoDBOpsManager):
        expected_roles = {
            ("admin", "readWriteAnyDatabase"),
            ("admin", "dbAdminAnyDatabase"),
            ("admin", "clusterMonitor"),
            ("admin", "hostManager"),
            ("admin", "backup"),
            ("admin", "restore"),
        }

        # only user should be the Ops Manager user
        tester = ops_manager.get_automation_config_tester()
        tester.assert_authentication_mechanism_enabled("SCRAM-SHA-256", False)
        tester.assert_has_user("mongodb-ops-manager")
        tester.assert_user_has_roles("mongodb-ops-manager", expected_roles)
        tester.assert_expected_users(1)
        tester.assert_authoritative_set(False)

    @skip_if_local
    def test_appdb_scram_sha(self, ops_manager: MongoDBOpsManager):
        app_db_tester = ops_manager.get_appdb_tester()
        app_db_tester.assert_scram_sha_authentication(
            "mongodb-ops-manager",
            ops_manager.read_appdb_generated_password(),
            auth_mechanism="SCRAM-SHA-256",
        )

    def test_appdb_mongodb_options(self, ops_manager: MongoDBOpsManager):
        automation_config_tester = ops_manager.get_automation_config_tester()
        for process in automation_config_tester.get_replica_set_processes(
            ops_manager.app_db_name()
        ):
            assert process["args2_6"]["operationProfiling"]["mode"] == "slowOp"

    def test_om_reaches_running(self, ops_manager: MongoDBOpsManager):
        ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=600)

    def test_appdb_reaches_running(self, ops_manager: MongoDBOpsManager):
        ops_manager.appdb_status().assert_abandons_phase(Phase.Running, timeout=100)
        ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=300)

    def test_appdb_monitoring_is_configured(self, ops_manager: MongoDBOpsManager):
        ops_manager.assert_appdb_monitoring_group_was_created()

    def test_om_running(self, ops_manager: MongoDBOpsManager):
        ops_manager.get_om_tester().assert_healthiness()

    # TODO check the persistent volumes created


@pytest.mark.e2e_om_appdb_upgrade
class TestOpsManagerAppDbUpgrade:
    """
    Upgrades appdb to the bundled version. The test waits until the AppDB is ready, not the OM resource
    """

    def test_appdb_bundled_version(self, ops_manager: MongoDBOpsManager):
        ops_manager.load()
        ops_manager.set_appdb_version("")
        ops_manager.update()
        ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=900)
        # Note, that we don't wait for "OM == reconciling" as this phase passes too quickly
        ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=100)

    def test_appdb(self, ops_manager: MongoDBOpsManager):
        assert ops_manager.appdb_status().get_members() == 3

    def test_admin_config_map(self, ops_manager: MongoDBOpsManager):
        ops_manager.get_automation_config_tester().reached_version(2)

    @skip_if_local
    def test_mongod(self, ops_manager: MongoDBOpsManager):
        mdb_tester = ops_manager.get_appdb_tester()
        mdb_tester.assert_connectivity()
        mdb_tester.assert_version(MongoDBOpsManager.get_bundled_appdb_version())
        mdb_tester.assert_is_enterprise()

    def test_om_is_running(self, ops_manager: MongoDBOpsManager):
        ops_manager.get_om_tester().assert_healthiness()


@pytest.mark.e2e_om_appdb_upgrade
class TestOpsManagerAppDbUpdateMemory:
    """
    Changes memory limits requirements for the AppDB
    """

    def test_appdb_updated(self, ops_manager: MongoDBOpsManager):
        ops_manager.load()
        ops_manager["spec"]["applicationDatabase"]["podSpec"] = {"memory": "350M"}
        ops_manager.update()
        ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=400)
        # Note, that we don't wait for "OM == reconciling" as this phase passes too quickly
        ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=100)

    def test_appdb(self, ops_manager: MongoDBOpsManager):
        db_pods = ops_manager.read_appdb_pods()
        for pod in db_pods:
            assert pod.spec.containers[0].resources.requests["memory"] == "350M"

    def test_admin_config_map(self, ops_manager: MongoDBOpsManager):
        # The version hasn't changed as there were no changes to the automation config
        ops_manager.get_automation_config_tester().reached_version(2)

    @skip_if_local
    def test_om_is_running(self, ops_manager: MongoDBOpsManager):
        ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=400)
        ops_manager.get_om_tester().assert_healthiness()


@pytest.mark.e2e_om_appdb_upgrade
class TestOpsManagerMixed:
    """
    Performs changes to both AppDB and Ops Manager spec
    """

    def test_appdb_and_om_updated(
        self, ops_manager: MongoDBOpsManager, custom_appdb_version: str
    ):
        ops_manager.load()
        ops_manager.set_appdb_version(custom_appdb_version)
        ops_manager["spec"]["configuration"] = {
            "mms.helpAndSupportPage.enabled": "true"
        }
        ops_manager.update()
        ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=400)
        ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=400)

        # no backup status
        assert ops_manager.backup_status().get_phase() is None

    def test_appdb(self, ops_manager: MongoDBOpsManager, custom_appdb_version: str):
        assert ops_manager.appdb_status().get_members() == 3
        assert ops_manager.appdb_status().get_version() == custom_appdb_version

    @skip_if_local
    def test_mongod(self, ops_manager: MongoDBOpsManager, custom_appdb_version: str):
        mdb_tester = ops_manager.get_appdb_tester()
        mdb_tester.assert_connectivity()
        mdb_tester.assert_version(custom_appdb_version)

    @skip_if_local
    def test_om_connectivity(self, ops_manager: MongoDBOpsManager):
        om_tester = ops_manager.get_om_tester()
        om_tester.assert_healthiness()
        om_tester.assert_support_page_enabled()
