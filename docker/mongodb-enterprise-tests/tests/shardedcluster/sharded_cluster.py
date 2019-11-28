import pytest

from kubetester.kubetester import KubernetesTester
from kubetester.mongotester import ShardedClusterTester
from kubernetes import client


@pytest.mark.e2e_sharded_cluster
class TestShardedClusterCreation(KubernetesTester):
    """
    name: Sharded Cluster Base Creation
    description: |
      Creates a simple Sharded Cluster with 1 shard, 2 mongos,
      1 replica set as config server and NO persistent volumes.
    create:
      file: sharded-cluster.yaml
      wait_until: in_running_state
      timeout: 360
    """

    def test_sharded_cluster_sts(self):
        sts0 = self.appsv1.read_namespaced_stateful_set("sh001-base-0", self.namespace)
        assert sts0

    def test_config_sts(self):
        config = self.appsv1.read_namespaced_stateful_set(
            "sh001-base-config", self.namespace
        )
        assert config

    def test_mongos_sts(self):
        mongos = self.appsv1.read_namespaced_stateful_set(
            "sh001-base-mongos", self.namespace
        )
        assert mongos

    def test_mongod_sharded_cluster_service(self):
        svc0 = self.corev1.read_namespaced_service("sh001-base-sh", self.namespace)
        assert svc0

    def test_shard0_was_configured(self):
        ShardedClusterTester("sh001-base", 1).assert_connectivity()

    def test_shard0_was_configured_with_srv(self):
        ShardedClusterTester("sh001-base", 1, ssl=False, srv=True).assert_connectivity()


@pytest.mark.e2e_sharded_cluster
class TestShardedClusterUpdate(KubernetesTester):
    """
    name: Sharded Cluster Base Creation
    description: |
      Scales a Sharded Cluster from 1 to 2 Shards
    update:
      file: sharded-cluster.yaml
      patch: '[{"op":"replace","path":"/spec/shardCount","value":2}]'
      wait_until: in_running_state
      timeout: 240
    """

    def test_shard1_was_configured(self):
        hosts = [
            "sh001-base-1-{}.sh001-base-sh.{}.svc.cluster.local:27017".format(
                i, self.namespace
            )
            for i in range(3)
        ]

        primary, secondaries = self.wait_for_rs_is_ready(hosts)
        assert primary is not None
        assert len(secondaries) == 2


@pytest.mark.e2e_sharded_cluster
class TestShardedClusterDeletion(KubernetesTester):
    """
    name: Sharded Cluster Base Deletion
    description: |
      Removes a Sharded Cluster
    delete:
      file: sharded-cluster.yaml
      wait_until: mongo_resource_deleted
      timeout: 240
    """

    def test_sharded_cluster_doesnt_exist(self):
        # There should be no statefulsets in this namespace
        sts = self.appsv1.list_namespaced_stateful_set(self.namespace)
        assert len(sts.items) == 0

    def test_service_does_not_exist(self):
        with pytest.raises(client.rest.ApiException):
            self.corev1.read_namespaced_service("sh001-base-sh", self.namespace)
