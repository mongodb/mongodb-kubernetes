import pytest

from kubetester.kubetester import KubernetesTester
from kubetester.mongotester import ReplicaSetTester, ShardedClusterTester


@pytest.mark.e2e_operator_upgrade_scale_and_verify_deployment
class TestBuildDeploymentShardedCluster(KubernetesTester):
    def test_sharded_cluster_is_alive(self):
        ShardedClusterTester("sh001-base", 1).assert_connectivity()


@pytest.mark.e2e_operator_upgrade_scale_and_verify_deployment
class TestBuildDeploymentReplicaSet(KubernetesTester):
    def test_replica_set_is_alive(self):
        ReplicaSetTester("my-replica-set", 3).assert_connectivity()
