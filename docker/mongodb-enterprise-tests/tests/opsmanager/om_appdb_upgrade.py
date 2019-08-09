import pytest
from kubetester.kubetester import skip_if_local
from kubetester.mongotester import ReplicaSetTester
from kubetester.omtester import OMTester

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
      Creates an Ops Manager instance with AppDB of size 3.
    create:
      file: om_appdb_upgrade.yaml
      wait_until: om_in_running_state
      timeout: 900
    """
    def test_appdb(self):
        assert self.om_cr.get_appdb_status()['members'] == 3
        assert self.om_cr.get_appdb_status()['version'] == '4.0.0'

    @skip_if_local
    def test_om_connectivity(self):
        OMTester(self.om_context).assert_healthiness()


# TODO upgrade appdb to 4.2
@pytest.mark.e2e_om_appdb_upgrade
class TestOpsManagerAppDbUpgrade(OpsManagerBase):
    """
    name: Ops Manager appdb version change
    description: |
      Upgrades appdb to a newer version
    update:
      file: om_appdb_upgrade.yaml
      patch: '[{"op":"replace","path":"/spec/applicationDatabase/version","value":"4.0.11"}]'
      wait_until: om_in_running_state
      timeout: 400
    """

    def test_appdb(self):
        assert self.om_cr.get_appdb_status()['members'] == 3
        assert self.om_cr.get_appdb_status()['version'] == '4.0.11'

    @skip_if_local
    def test_mongod(self):
        mdb_tester = ReplicaSetTester(self.om_cr.app_db_name(), 3)
        mdb_tester.assert_connectivity()
        mdb_tester.assert_version('4.0.11')

    @skip_if_local
    def test_om_connectivity(self):
        OMTester(self.om_context).assert_healthiness()

@pytest.mark.e2e_om_appdb_upgrade
class TestOpsManagerAppDbUpdateMemory(OpsManagerBase):
    """
    name: Ops Manager appdb pod spec change
    description: |
      Changes memory requirements for the AppDB
    update:
      file: om_appdb_upgrade.yaml
      patch: '[{"op":"add","path":"/spec/applicationDatabase","value": {"podSpec": { "memory": "200M" }}}]'
      wait_until: om_in_running_state
      timeout: 400
    """

    def test_appdb(self):
        assert self.om_cr.get_appdb_status()['members'] == 3
        response = self.corev1.list_namespaced_pod(self.namespace)
        db_pods = [pod for pod in response.items if pod.metadata.name.startswith(self.om_cr.app_db_name())]
        for pod in db_pods:
            assert pod.spec.containers[0].resources.requests["memory"] == '200M'

    @skip_if_local
    def test_om_connectivity(self):
        OMTester(self.om_context).assert_healthiness()
