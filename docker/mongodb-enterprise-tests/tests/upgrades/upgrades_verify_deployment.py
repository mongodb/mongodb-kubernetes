import pytest

from kubetester.kubetester import KubernetesTester
from kubetester.mongotester import ReplicaSetTester, ShardedClusterTester


@pytest.mark.e2e_operator_upgrade_scale_and_verify_deployment
class TestBuildDeploymentShardedCluster(KubernetesTester):
    def test_sharded_cluster_is_alive(self):
        resource = ShardedClusterTester("sh001-base", 1)
        resource.assert_connectivity(attempts=5)
        resource.assert_version("4.0.3")


@pytest.mark.e2e_operator_upgrade_scale_and_verify_deployment
class TestBuildDeploymentReplicaSet(KubernetesTester):
    def test_replica_set_is_alive(self):
        resource = ReplicaSetTester("my-replica-set", 3)
        resource.assert_connectivity(wait_for=180, attempts=5)
        resource.assert_version("3.6.9")
