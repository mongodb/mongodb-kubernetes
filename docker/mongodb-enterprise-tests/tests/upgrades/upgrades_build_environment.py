import pytest

from kubetester.kubetester import KubernetesTester


@pytest.mark.e2e_operator_upgrade_build_deployment
class TestBuildDeploymentShardedCluster(KubernetesTester):
    """
    name: Sharded Cluster Base Creation for Upgrades
    description: |
      Creates a simple Sharded Cluster with 1 shard, 2 mongos,
      1 replica set as config server and NO persistent volumes.
      This test must be run in the Operator before version 1.2.3 as the latter
      one forbids creating more than one resource in the project
    create:
      file: sharded-cluster.yaml
      wait_until: in_running_state
      timeout: 360

    """

    def test_resource_has_no_warnings(self):
        mdb = KubernetesTester.get_namespaced_custom_object(self.get_namespace(), "sh001-base", "MongoDB")
        assert "warning" not in mdb["status"]


@pytest.mark.e2e_operator_upgrade_build_deployment
class TestBuildDeploymentReplicaSet(KubernetesTester):
    '''
    name: Replica Set Creation for Upgrades
    description: |
      Creates a Replica set and checks everything is created as expected.
    create:
      file: replica-set.yaml
      wait_until: in_running_state
      timeout: 240
    '''

    def test_resource_has_no_warnings(self):
        mdb = KubernetesTester.get_namespaced_custom_object(self.get_namespace(), "my-replica-set", "MongoDB")
        assert "warning" not in mdb["status"]
