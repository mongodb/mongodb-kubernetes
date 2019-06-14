import pytest

from kubetester.kubetester import KubernetesTester


@pytest.mark.e2e_operator_upgrade_build_deployment
class TestBuildDeploymentShardedCluster(KubernetesTester):
    """
    name: Sharded Cluster Base Creation for Upgrades
    description: |
      Creates a simple Sharded Cluster with 1 shard, 2 mongos,
      1 replica set as config server and NO persistent volumes.
    create:
      file: sharded-cluster.yaml
      wait_until: in_running_state
    """

    def test_noop(self):
        assert True


@pytest.mark.e2e_operator_upgrade_build_deployment
class TestBuildDeploymentReplicaSet(KubernetesTester):
    '''
    name: Replica Set Creation for Upgrades
    description: |
      Creates a Replica set and checks everything is created as expected.
    create:
      file: replica-set.yaml
      wait_until: in_running_state
    '''

    def test_noop(self):
        assert True
