import pytest
from kubetester import create_or_update
from kubetester.automation_config_tester import AutomationConfigTester
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as load_fixture
from kubetester.mongodb import MongoDB, Phase
from kubetester.mongotester import ReplicaSetTester

MDB_RESOURCE = "my-replica-set-scram"


@pytest.mark.e2e_replica_set_scram_sha_1_upgrade
class TestCreateScramSha1ReplicaSet(KubernetesTester):

    def test_create_replicaset(self, custom_mdb_version: str):
        resource = MongoDB.from_yaml(load_fixture("replica-set-scram.yaml"), namespace=self.namespace)
        resource.set_version(custom_mdb_version)
        create_or_update(resource)

        resource.assert_reaches_phase(Phase.Running)

    def test_assert_connectivity(self):
        ReplicaSetTester(MDB_RESOURCE, 3).assert_connectivity()

    def test_ops_manager_state_updated_correctly(self):
        tester = AutomationConfigTester(KubernetesTester.get_automation_config())
        tester.assert_authentication_mechanism_enabled("SCRAM-SHA-256")
        tester.assert_authentication_enabled()

        tester.assert_expected_users(0)
        tester.assert_authoritative_set(True)


@pytest.mark.e2e_replica_set_scram_sha_1_upgrade
class TestReplicaSetDeleted(KubernetesTester):
    """
    description: |
      Deletes the Replica Set.
    delete:
      file: replica-set-scram.yaml
      wait_until: mongo_resource_deleted
      timeout: 120
    """

    def test_authentication_was_disabled(self):
        def authentication_was_disabled():
            tester = AutomationConfigTester(KubernetesTester.get_automation_config())
            try:
                tester.assert_authentication_disabled()
                return True
            except AssertionError:
                return False

        KubernetesTester.wait_until(authentication_was_disabled, timeout=10, sleep_time=1)
