import json

import pytest
from kubetester.kubetester import skip_if_local
from kubetester.omtester import OMTester
from kubetester.automation_config_tester import AutomationConfigTester

from tests.opsmanager.om_base import OpsManagerBase

gen_key_resource_version = None
admin_key_resource_version = None


# Note the strategy for Ops Manager testing: the tests should have more than 1 updates - this is because the initial
# creation of Ops Manager takes too long, so we try to avoid fine-grained test cases and combine different
# updates in one test


@pytest.mark.e2e_om_appdb_upgrade
class TestOpsManagerCreation(OpsManagerBase):
    """
    name: Ops Manager successful creation
    description: |
      Creates an Ops Manager instance with AppDB of size 3. The test waits until the AppDB is ready, not the OM resource
    create:
      file: om_appdb_upgrade.yaml
      wait_until: appdb_in_running_state
      timeout: 400
    """

    def test_appdb(self):
        assert self.om_cr.get_appdb_status()["members"] == 3
        assert self.om_cr.get_appdb_status()["version"] == "4.0.0"
        response = self.corev1.list_namespaced_pod(self.namespace)
        db_pods = [
            pod
            for pod in response.items
            if pod.metadata.name.startswith(self.om_cr.app_db_name())
        ]
        for pod in db_pods:
            # the appdb pods by default have 500M
            assert pod.spec.containers[0].resources.requests["memory"] == "500M"

    def test_admin_config_map(self):
        config_map = self.corev1.read_namespaced_config_map(
            self.om_cr.app_config_name(), self.namespace
        ).data
        assert json.loads(config_map["cluster-config.json"])["version"] == 1

    @skip_if_local
    def test_mongod(self):
        mdb_tester = self.om_cr.get_appdb_mongo_tester()
        mdb_tester.assert_connectivity()
        mdb_tester.assert_version("4.0.0")

        # then we need to wait until Ops Manager is ready (only AppDB is ready so far) for the next test
        self.wait_until("om_in_running_state", 900)

    def test_appdb_automation_config(self):
        expected_roles = {
            ("admin", "readWriteAnyDatabase"),
            ("admin", "dbAdminAnyDatabase"),
            ("admin", "clusterMonitor"),
        }

        # only user should be the Ops Manager user
        tester = AutomationConfigTester(
            self.get_appdb_automation_config(),
            expected_users=1,
            authoritative_set=False,
        )
        tester.assert_authentication_mechanism_enabled("MONGODB-CR")
        tester.assert_has_user("mongodb-ops-manager")
        tester.assert_user_has_roles("mongodb-ops-manager", expected_roles)

    @skip_if_local
    def test_appdb_scram_sha(self):
        app_db_tester = self.om_cr.get_appdb_mongo_tester()
        app_db_tester.assert_scram_sha_authentication(
            "mongodb-ops-manager",
            self.get_appdb_password(),
            auth_mechanism="SCRAM-SHA-1",
        )

    # TODO check the persistent volumes created


@pytest.mark.e2e_om_appdb_upgrade
class TestOpsManagerAppDbUpgrade(OpsManagerBase):
    """
    name: Ops Manager appdb version change
    description: |
      Upgrades appdb to a newer version. The test waits until the AppDB is ready, not the OM resource
    update:
      file: om_appdb_upgrade.yaml
      patch: '[{"op":"replace","path":"/spec/applicationDatabase/version","value":"4.2.0"}]'
      wait_until: appdb_in_running_state
      timeout: 400
    """

    def test_appdb(self):
        assert self.om_cr.get_appdb_status()["members"] == 3
        assert self.om_cr.get_appdb_status()["version"] == "4.2.0"

    def test_admin_config_map(self):
        config_map = self.corev1.read_namespaced_config_map(
            self.om_cr.app_config_name(), self.namespace
        ).data
        assert json.loads(config_map["cluster-config.json"])["version"] == 2

    @skip_if_local
    def test_mongod(self):
        mdb_tester = self.om_cr.get_appdb_mongo_tester()
        mdb_tester.assert_connectivity()
        mdb_tester.assert_version("4.2.0")

        # then we need to wait until Ops Manager is ready (only AppDB is ready so far) for the next test

        self.wait_until("om_in_running_state", 900)


@pytest.mark.e2e_om_appdb_upgrade
class TestOpsManagerAppDbUpdateMemory(OpsManagerBase):
    """
    name: Ops Manager appdb pod spec change
    description: |
      Changes memory limits requirements for the AppDB
    update:
      file: om_appdb_upgrade.yaml
      patch: '[{"op":"add","path":"/spec/applicationDatabase","value": {"podSpec": { "memory": "350M" }}}]'
      wait_until: om_in_running_state
      timeout: 600
    """

    def test_appdb(self):
        assert self.om_cr.get_appdb_status()["members"] == 3
        response = self.corev1.list_namespaced_pod(self.namespace)
        db_pods = [
            pod
            for pod in response.items
            if pod.metadata.name.startswith(self.om_cr.app_db_name())
        ]
        for pod in db_pods:
            assert pod.spec.containers[0].resources.requests["memory"] == "350M"

    def test_admin_config_map(self):
        config_map = self.corev1.read_namespaced_config_map(
            self.om_cr.app_config_name(), self.namespace
        ).data
        # The version hasn't changed as there were no changes to the automation config
        assert json.loads(config_map["cluster-config.json"])["version"] == 2

    @skip_if_local
    def test_om_connectivity(self):
        OMTester(self.om_context).assert_healthiness()


@pytest.mark.e2e_om_appdb_upgrade
class TestOpsManagerMixed(OpsManagerBase):
    """
    name: Ops Manager mixed scenario
    description: |
      Performs changes to both AppDB and Ops Manager spec
    update:
      file: om_appdb_upgrade.yaml
      patch: '[{"op":"replace","path":"/spec/applicationDatabase/version","value":"4.2.1"},{"op":"add","path":"/spec/configuration","value":{"mms.helpAndSupportPage.enabled":"true"}}]'
      wait_until: om_in_running_state
      timeout: 600
    """

    def test_appdb(self):
        assert self.om_cr.get_appdb_status()["members"] == 3
        assert self.om_cr.get_appdb_status()["version"] == "4.2.1"

    @skip_if_local
    def test_mongod(self):
        mdb_tester = self.om_cr.get_appdb_mongo_tester()
        mdb_tester.assert_connectivity()
        mdb_tester.assert_version("4.2.1")

    @skip_if_local
    def test_om_connectivity(self):
        om_tester = OMTester(self.om_context)
        om_tester.assert_healthiness()
        om_tester.assert_support_page_enabled()
