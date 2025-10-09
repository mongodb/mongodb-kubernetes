import kubernetes
import pytest
from kubetester import try_load
from kubetester.kubetester import ensure_ent_version
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.kubetester import is_multi_cluster
from kubetester.mongodb import MongoDB
from kubetester.operator import Operator
from kubetester.phase import Phase
from pytest import fixture, mark
from tests.shardedcluster.conftest import (
    enable_multi_cluster_deployment,
    get_member_cluster_clients_using_cluster_mapping,
    get_mongos_service_names,
)


@fixture(scope="function")
def sc(namespace: str, custom_mdb_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("sharded-cluster-scale-shards.yaml"),
        namespace=namespace,
    )

    if try_load(resource):
        return resource

    resource.set_version(ensure_ent_version(custom_mdb_version))
    resource.set_architecture_annotation()

    # this test requires the volumes to not be persistent as we first scale shard down and then scale up without clearing PV
    # in order to get rid of persistent: False we should add removing PV here
    resource["spec"]["persistent"] = False

    if is_multi_cluster():
        enable_multi_cluster_deployment(
            resource,
            shard_members_array=[1, 1, 1],
            mongos_members_array=[1, None, None],
            configsrv_members_array=[None, 1, None],
        )

    return resource.update()


@mark.e2e_sharded_cluster_scale_shards
def test_install_operator(operator: Operator):
    operator.assert_is_running()


@mark.e2e_sharded_cluster_scale_shards
class TestShardedClusterScaleShardsCreate:
    """
    name: ShardedCluster scale of shards (create)
    description: |
      Creates a sharded cluster with 2 shards
    """

    def test_sharded_cluster_running(self, sc: MongoDB):
        sc.assert_reaches_phase(Phase.Running, timeout=1000)

    def test_db_connectable(self, sc: MongoDB):
        service_names = get_mongos_service_names(sc)
        mongod_tester = sc.tester(service_names=service_names)

        mongod_tester.shard_collection(f"{sc.name}-{{}}", 2, "type")
        mongod_tester.upload_random_data(50_000)
        mongod_tester.prepare_for_shard_removal(f"{sc.name}-{{}}", 2)
        mongod_tester.assert_number_of_shards(2)
        # todo would be great to verify that chunks are distributed over shards, but I didn't manage to get the same
        # results as from CMD sh.status()(alisovenko)
        # self.client.config.command('printShardingStatus') --> doesn't work


@mark.e2e_sharded_cluster_scale_shards
class TestShardedClusterScaleDownShards:
    """
    name: ShardedCluster scale down of shards (update)
    description: |
      Updates the sharded cluster, scaling down its shards count to 1. Makes sure no data is lost.
      (alisovenko) Implementation notes: I tried to get long rebalancing to make sure it's covered with multiple reconciliations,
      but in fact rebalancing is almost immediate (insertion is way longer) so the single reconciliation manages to get
      agents
    """

    def test_scale_down_sharded_cluster(self, sc: MongoDB):
        sc["spec"]["shardCount"] = 1
        sc.update()

        sc.assert_reaches_phase(Phase.Running, timeout=1000)

    def test_db_data_the_same_count(self, sc: MongoDB):
        service_names = get_mongos_service_names(sc)
        mongod_tester = sc.tester(service_names=service_names)

        mongod_tester.assert_number_of_shards(1)
        mongod_tester.assert_data_size(50_000)

    def test_statefulset_for_shard_removed(self, sc: MongoDB):
        for cluster_member_client in get_member_cluster_clients_using_cluster_mapping(sc.name, sc.namespace):
            cluster_idx = cluster_member_client.cluster_index
            with pytest.raises(kubernetes.client.ApiException) as api_exception:
                shard_sts_name = sc.shard_statefulset_name(1, cluster_idx)
                cluster_member_client.read_namespaced_stateful_set(shard_sts_name, sc.namespace)
            assert api_exception.value.status == 404


@mark.e2e_sharded_cluster_scale_shards
class TestShardedClusterScaleUpShards:
    """
    name: ShardedCluster scale up  of shards (sc)
    description: |
      Updates the sharded cluster, scaling up its shards count to 2. Makes sure no data is lost.
    """

    def test_scale_up_sharded_cluster(self, sc: MongoDB):
        sc.load()
        sc["spec"]["shardCount"] = 2
        sc.update()

        sc.assert_reaches_phase(Phase.Running, timeout=1000)

    def test_db_data_the_same_count(self, sc: MongoDB):
        service_names = get_mongos_service_names(sc)
        mongod_tester = sc.tester(service_names=service_names)

        mongod_tester.assert_number_of_shards(2)
        mongod_tester.assert_data_size(50_000)

    def test_statefulset_for_shard_added(self, sc: MongoDB):
        for cluster_member_client in get_member_cluster_clients_using_cluster_mapping(sc.name, sc.namespace):
            cluster_idx = cluster_member_client.cluster_index

            shard_sts_name = sc.shard_statefulset_name(1, cluster_idx)
            shard_sts = cluster_member_client.read_namespaced_stateful_set(shard_sts_name, sc.namespace)
            assert shard_sts is not None
