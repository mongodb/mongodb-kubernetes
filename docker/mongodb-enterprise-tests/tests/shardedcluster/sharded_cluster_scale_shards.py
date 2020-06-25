import pytest
from kubetester.kubetester import KubernetesTester
from kubetester.mongotester import ShardedClusterTester
from kubernetes import client


@pytest.mark.e2e_sharded_cluster_scale_shards
class TestShardedClusterScaleShardsCreate(KubernetesTester):
    """
    name: ShardedCluster scale of shards (create)
    description: |
      Creates a sharded cluster with 2 shards
    create:
      file: sharded-cluster-scale-shards.yaml
      wait_until: in_running_state
      timeout: 240
    """

    def test_db_connectable(self):
        mongod_tester = ShardedClusterTester("sh001-scale-down-shards", 1)
        mongod_tester.shard_collection("sh001-scale-down-shards-{}", 2, "type")
        mongod_tester.upload_random_data(50_000)
        mongod_tester.prepare_for_shard_removal("sh001-scale-down-shards-{}", 2)
        mongod_tester.assert_number_of_shards(2)
        # todo would be great to verify that chunks are distributed over shards, but I didn't manage to get the same
        # results as from CMD sh.status()(alisovenko)
        # self.client.config.command('printShardingStatus') --> doesn't work


@pytest.mark.e2e_sharded_cluster_scale_shards
class TestShardedClusterScaleDownShards(KubernetesTester):
    """
    name: ShardedCluster scale down of shards (update)
    description: |
      Updates the sharded cluster, scaling down its shards count to 1. Makes sure no data is lost.
      (alisovenko) Implementation notes: I tried to get long rebalancing to make sure it's covered with multiple reconciliations,
      but in fact rebalancing is almost immediate (insertion is way longer) so the single reconciliation manages to get
      agents
    update:
      file: sharded-cluster-scale-shards.yaml
      patch: '[{"op":"replace","path":"/spec/shardCount", "value": 1}]'
      wait_until: in_running_state
      timeout: 360
    """

    def test_db_data_the_same_count(self):
        mongod_tester = ShardedClusterTester("sh001-scale-down-shards", 1)

        mongod_tester.assert_number_of_shards(1)
        mongod_tester.assert_data_size(50_000)

    def test_statefulset_for_shard_removed(self):
        with pytest.raises(client.rest.ApiException):
            self.appsv1.read_namespaced_stateful_set(
                "sh001-scale-down-shards-1", self.namespace
            )


@pytest.mark.e2e_sharded_cluster_scale_shards
class TestShardedClusterScaleUpShards(KubernetesTester):
    """
    name: ShardedCluster scale down of shards (sc)
    description: |
      Updates the sharded cluster, scaling up its shards count to 2. Makes sure no data is lost.
    update:
      file: sharded-cluster-scale-shards.yaml
      patch: '[{"op":"replace","path":"/spec/shardCount", "value": 2}]'
      wait_until: in_running_state
      timeout: 360
    """

    def test_db_data_the_same_count(self):
        mongod_tester = ShardedClusterTester("sh001-scale-down-shards", 1)

        mongod_tester.assert_number_of_shards(2)
        mongod_tester.assert_data_size(50_000)

    def test_statefulset_for_shard_added(self):
        assert (
            self.appsv1.read_namespaced_stateful_set(
                "sh001-scale-down-shards-1", self.namespace
            )
            is not None
        )
