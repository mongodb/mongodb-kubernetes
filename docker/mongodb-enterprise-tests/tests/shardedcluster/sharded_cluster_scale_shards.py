import pytest
from kubernetes import client
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as load_fixture
from kubetester.mongodb import MongoDB, Phase
from kubetester.mongotester import ShardedClusterTester


@pytest.fixture(scope="module")
def sc(namespace: str, custom_mdb_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(load_fixture("sharded-cluster-scale-shards.yaml"), namespace=namespace)
    resource.set_version(custom_mdb_version)
    return resource.update()


@pytest.mark.e2e_sharded_cluster_scale_shards
class TestShardedClusterScaleShardsCreate(KubernetesTester):
    """
    name: ShardedCluster scale of shards (create)
    description: |
      Creates a sharded cluster with 2 shards
    """

    def test_sharded_cluster_running(self, sc: MongoDB):
        sc.assert_reaches_phase(Phase.Running)

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
    """

    def test_scale_down_sharded_cluster(self, sc: MongoDB):
        sc.load()
        sc["spec"]["shardCount"] = 1
        sc.update()

        sc.assert_reaches_phase(Phase.Running)

    def test_db_data_the_same_count(self):
        mongod_tester = ShardedClusterTester("sh001-scale-down-shards", 1)

        mongod_tester.assert_number_of_shards(1)
        mongod_tester.assert_data_size(50_000)

    def test_statefulset_for_shard_removed(self):
        with pytest.raises(client.rest.ApiException):
            self.appsv1.read_namespaced_stateful_set("sh001-scale-down-shards-1", self.namespace)


@pytest.mark.e2e_sharded_cluster_scale_shards
class TestShardedClusterScaleUpShards(KubernetesTester):
    """
    name: ShardedCluster scale up  of shards (sc)
    description: |
      Updates the sharded cluster, scaling up its shards count to 2. Makes sure no data is lost.
    """

    def test_scale_up_sharded_cluster(self, sc: MongoDB):
        sc.load()
        sc["spec"]["shardCount"] = 2
        sc.update()

        sc.assert_reaches_phase(Phase.Running)

    def test_db_data_the_same_count(self):
        mongod_tester = ShardedClusterTester("sh001-scale-down-shards", 1)

        mongod_tester.assert_number_of_shards(2)
        mongod_tester.assert_data_size(50_000)

    def test_statefulset_for_shard_added(self):
        assert self.appsv1.read_namespaced_stateful_set("sh001-scale-down-shards-1", self.namespace) is not None
