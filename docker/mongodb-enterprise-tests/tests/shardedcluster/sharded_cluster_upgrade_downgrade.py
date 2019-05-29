import pytest
from kubetester.kubetester import KubernetesTester

@pytest.mark.e2e_sharded_cluster_upgrade_downgrade
class TestShardedClusterUpgradeDowngradeCreate(KubernetesTester):
    '''
    name: ShardedCluster upgrade downgrade (create)
    description: |
      Creates a sharded cluster, then upgrades it with compatibility version set and then downgrades back
    create:
      file: sharded-cluster-downgrade.yaml
      wait_until: in_running_state
      timeout: 200
    '''

    def test_db_connectable(self):
        self.check_mongoses_are_ready("sh001-downgrade", expected_version="3.6.5", mongos_count=1)

@pytest.mark.e2e_sharded_cluster_upgrade_downgrade
class TestShardedClusterUpgradeDowngradeUpdate(KubernetesTester):
    '''
    name: ShardedCluster upgrade downgrade (update)
    description: |
      Updates a ShardedCluster to bigger version, leaving feature compatibility version as it was
    update:
      file: sharded-cluster-downgrade.yaml
      patch: '[{"op":"replace","path":"/spec/version", "value": "4.0.7"}, {"op":"add","path":"/spec/featureCompatibilityVersion", "value": "3.6"}]'
      wait_until: in_running_state
      timeout: 200
    '''

    def test_db_connectable(self):
        self.check_mongoses_are_ready("sh001-downgrade", expected_version="4.0.7", mongos_count=1)

@pytest.mark.e2e_sharded_cluster_upgrade_downgrade
class TestShardedClusterUpgradeDowngradeRevert(KubernetesTester):
    '''
    name: ShardedCluster upgrade downgrade (downgrade)
    description: |
      Updates a ShardedCluster to the same version it was created initially
    update:
      file: sharded-cluster-downgrade.yaml
      wait_until: in_running_state
      timeout: 200
    '''

    def test_db_connectable(self):
        self.check_mongoses_are_ready("sh001-downgrade", expected_version="3.6.5", mongos_count=1)
