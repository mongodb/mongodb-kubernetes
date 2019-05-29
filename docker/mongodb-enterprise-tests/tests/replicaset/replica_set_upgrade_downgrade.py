import pytest
from kubetester.kubetester import KubernetesTester


# TODO change 3.6 -> 4.0 upgrade to 4.0 -> 4.2 when mongodb is released
@pytest.mark.e2e_replica_set_upgrade_downgrade
class TestReplicaSetUpgradeDowngradeCreate(KubernetesTester):
    '''
    name: ReplicaSet upgrade downgrade (create)
    description: |
      Creates a replica set, then upgrades it with compatibility version set and then downgrades back
    create:
      file: replica-set-downgrade.yaml
      wait_until: in_running_state
      timeout: 150
    '''


    def test_db_connectable(self):
        primary_available, secondaries_available = self.check_replica_set_is_ready("my-replica-set-downgrade", expected_version="3.6.0")

        assert primary_available, "primary was not available"
        assert secondaries_available, "secondaries not available"

@pytest.mark.e2e_replica_set_upgrade_downgrade
class TestReplicaSetUpgradeDowngradeUpdate(KubernetesTester):
    '''
    name: ReplicaSet upgrade downgrade (update)
    description: |
      Updates a ReplicaSet to bigger version, leaving feature compatibility version as it was
    update:
      file: replica-set-downgrade.yaml
      patch: '[{"op":"replace","path":"/spec/version", "value": "4.0.9"}, {"op":"add","path":"/spec/featureCompatibilityVersion", "value": "3.6"}]'
      wait_until: in_running_state
      timeout: 150
    '''

    def test_db_connectable(self):
        primary_available, secondaries_available = self.check_replica_set_is_ready("my-replica-set-downgrade", expected_version="4.0.9")

        assert primary_available, "primary was not available"
        assert secondaries_available, "secondaries not available"

@pytest.mark.e2e_replica_set_upgrade_downgrade
class TestReplicaSetUpgradeDowngradeRevert(KubernetesTester):
    '''
    name: ReplicaSet upgrade downgrade (downgrade)
    description: |
      Updates a ReplicaSet to the same version it was created initially
    update:
      file: replica-set-downgrade.yaml
      wait_until: in_running_state
      timeout: 150
    '''

    def test_db_connectable(self):
        primary_available, secondaries_available = self.check_replica_set_is_ready("my-replica-set-downgrade", expected_version="3.6.0")

        assert primary_available, "primary was not available"
        assert secondaries_available, "secondaries not available"
