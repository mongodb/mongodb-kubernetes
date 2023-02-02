import pytest

from kubetester.kubetester import KubernetesTester
from kubetester.mongotester import ReplicaSetTester
from kubetester.automation_config_tester import AutomationConfigTester

MDB_RESOURCE = "my-replica-set-scram-sha-1"


@pytest.mark.e2e_replica_set_scram_sha_1_upgrade
class TestCreateScramSha1ReplicaSet(KubernetesTester):
    """
    description: |
      Creates a Replica Set with SCRAM-SHA-1 authentication
    create:
      file: replica-set-scram-sha-1.yaml
      wait_until: in_running_state
    """

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
      file: replica-set-scram-sha-1.yaml
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

        KubernetesTester.wait_until(
            authentication_was_disabled, timeout=10, sleep_time=1
        )
