import pytest

from kubetester.kubetester import KubernetesTester
from kubetester.mongotester import ReplicaSetTester, ShardedClusterTester


@pytest.mark.e2e_operator_upgrade_scale_and_verify_deployment
class TestBuildDeploymentShardedCluster(KubernetesTester):
    """
    name: Wait for the Sharded Cluster to reach goal state.
    update:
      file: sharded-cluster.yaml
      patch: '{}'
      wait_until: in_running_state
    """
    def test_resource_has_warnings_set(self):
        mdb = KubernetesTester.get_namespaced_custom_object(self.get_namespace(), "sh001-base", "MongoDB")
        assert mdb["status"]["warnings"][0] == "Project contains multiple clusters"

    def test_sharded_cluster_is_alive(self):
        resource = ShardedClusterTester("sh001-base", 1)
        resource.assert_connectivity(attempts=5)
        resource.assert_version("4.0.3")


@pytest.mark.e2e_operator_upgrade_scale_and_verify_deployment
class TestBuildDeploymentReplicaSet(KubernetesTester):
    '''
    name: Wait for the Replica Set to reach goal state.
    update:
      file: replica-set.yaml
      patch: '{}'
      wait_until: in_running_state
    '''
    def test_resource_has_warnings_set(self):
        mdb = KubernetesTester.get_namespaced_custom_object(self.get_namespace(), "my-replica-set", "MongoDB")
        assert mdb["status"]["warnings"][0] == "Project contains multiple clusters"

    def test_replica_set_is_alive(self):
        resource = ReplicaSetTester("my-replica-set", 3)
        resource.assert_connectivity(wait_for=180, attempts=5)
        resource.assert_version("3.6.9")
