import pytest
from kubernetes import client
from kubetester.kubetester import fixture as _fixture
from kubetester.mongodb import MongoDB, Phase
from kubetester.operator import Operator
from pytest import fixture, mark


@fixture(scope="module")
def sharded_cluster(namespace: str) -> MongoDB:
    resource = MongoDB.from_yaml(_fixture("sharded-cluster-scale-down-shards.yaml"), namespace=namespace)
    return resource.create()


@mark.e2e_sharded_cluster_scale_down_shards
def test_install_operator(default_operator: Operator):
    default_operator.assert_is_running()


@mark.e2e_sharded_cluster_scale_down_shards
def test_db_connectable(sharded_cluster: MongoDB):
    sharded_cluster.assert_reaches_phase(Phase.Running, timeout=300)

    mongod_tester = sharded_cluster.tester()
    mongod_tester.shard_collection("sh001-scale-down-shards-{}", 2, "type")
    mongod_tester.upload_random_data(50_000)
    mongod_tester.prepare_for_shard_removal("sh001-scale-down-shards-{}", 2)
    mongod_tester.assert_number_of_shards(2)
    # todo would be great to verify that chunks are distributed over shards, but I didn't manage to get the same
    # results as from CMD sh.status()(alisovenko)
    # self.client.config.command('printShardingStatus') --> doesn't work


@mark.e2e_sharded_cluster_scale_down_shards
def test_db_data_the_same_count(sharded_cluster: MongoDB):
    """
    Updates the sharded cluster, scaling down its shards count to 1. Makes sure no data is lost.
    """
    sharded_cluster.load()
    sharded_cluster["spec"]["shardCount"] = 1
    sharded_cluster["spec"]["mongodsPerShardCount"] = 1
    sharded_cluster["spec"]["mongosCount"] = 1
    sharded_cluster["spec"]["configServerCount"] = 1
    sharded_cluster.update()

    sharded_cluster.assert_reaches_phase(Phase.Running, timeout=1200)

    mongod_tester = sharded_cluster.tester()
    mongod_tester.assert_number_of_shards(1)
    mongod_tester.assert_data_size(50_000)


@mark.e2e_sharded_cluster_scale_down_shards
def test_statefulset_for_shard_removed(namespace: str):
    with pytest.raises(client.rest.ApiException):
        client.AppsV1Api().read_namespaced_stateful_set("sh001-scale-down-shards-1", namespace)
