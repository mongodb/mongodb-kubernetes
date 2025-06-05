from kubetester import find_fixture, try_load
from kubetester.kubetester import KubernetesTester
from kubetester.mongodb import MongoDB
from kubetester.operator import Operator
from kubetester.phase import Phase
from pytest import fixture, mark
from tests import test_logger
from tests.conftest import get_member_cluster_names
from tests.multicluster.conftest import cluster_spec_list
from tests.multicluster_shardedcluster import (
    assert_correct_automation_config_after_scaling,
    assert_shard_sts_members_count,
    validate_member_count_in_ac,
    validate_shard_configurations_in_ac_multi,
)

MDB_RESOURCE_NAME = "sh-scaling-shard-overrides"
logger = test_logger.get_test_logger(__name__)


@fixture(scope="module")
def sc(namespace: str, custom_mdb_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        find_fixture("sharded-cluster-multi-cluster.yaml"), namespace=namespace, name=MDB_RESOURCE_NAME
    )

    if try_load(resource):
        return resource

    resource.set_architecture_annotation()

    return resource


@mark.e2e_multi_cluster_sharded_scaling_all_shard_overrides
class TestShardedClusterScalingInitial:

    def test_deploy_operator(self, multi_cluster_operator: Operator):
        multi_cluster_operator.assert_is_running()

    def test_create(self, sc: MongoDB, custom_mdb_version: str, issuer_ca_configmap: str):
        sc["spec"]["shardCount"] = 3
        # The distribution below is the default one but won't be used as all shards are overridden
        sc["spec"]["shard"]["clusterSpecList"] = cluster_spec_list(get_member_cluster_names(), [1, 2, 2])
        sc["spec"]["configSrv"]["clusterSpecList"] = cluster_spec_list(get_member_cluster_names(), [1, 1, 1])
        sc["spec"]["mongos"]["clusterSpecList"] = cluster_spec_list(get_member_cluster_names(), [1, 1, 1])

        # All shards contain overrides
        sc["spec"]["shardOverrides"] = [
            {
                "shardNames": [f"{sc.name}-0"],
                "clusterSpecList": cluster_spec_list(get_member_cluster_names(), [1, 2, 0]),
            },
            {
                "shardNames": [f"{sc.name}-1", f"{sc.name}-2"],
                "clusterSpecList": cluster_spec_list(get_member_cluster_names(), [0, 1, 2]),
            },
        ]

        sc.update()

    def test_sharded_cluster(self, sc: MongoDB):
        sc.assert_reaches_phase(Phase.Running, timeout=1600)

    def test_assert_correct_automation_config_after_scaling(self, sc: MongoDB):
        logger.info("Validating automation config correctness")
        assert_correct_automation_config_after_scaling(sc)

    def test_assert_stateful_sets_after_scaling(self, sc: MongoDB):
        logger.info("Validating statefulsets in cluster(s)")
        assert_shard_sts_members_count(sc, [[1, 2, 0], [0, 1, 2], [0, 1, 2]])


@mark.e2e_multi_cluster_sharded_scaling_all_shard_overrides
class TestShardedClusterScalingShardOverrides:

    def test_scale_overrides(self, sc: MongoDB, custom_mdb_version: str, issuer_ca_configmap: str):
        sc["spec"]["shardOverrides"] = [
            {
                "shardNames": [f"{sc.name}-0"],
                # cluster 3: 0->1
                "clusterSpecList": cluster_spec_list(get_member_cluster_names(), [1, 2, 1]),
            },
            {
                # cluster 1: 0->2
                "shardNames": [f"{sc.name}-1"],
                "clusterSpecList": cluster_spec_list(get_member_cluster_names(), [2, 1, 2]),
            },
            {
                # cluster 1: 0->1
                "shardNames": [f"{sc.name}-2"],
                "clusterSpecList": cluster_spec_list(get_member_cluster_names(), [1, 1, 2]),
            },
        ]

        sc.update()

    def test_sharded_cluster(self, sc: MongoDB):
        sc.assert_reaches_phase(Phase.Running, timeout=1600)

    def test_assert_correct_automation_config_after_scaling(self, sc: MongoDB):
        logger.info("Validating automation config correctness")
        assert_correct_automation_config_after_scaling(sc)

    def test_assert_stateful_sets_after_scaling(self, sc: MongoDB):
        logger.info("Validating statefulsets in cluster(s)")
        assert_shard_sts_members_count(sc, [[1, 2, 1], [2, 1, 2], [1, 1, 2]])


@mark.e2e_multi_cluster_sharded_scaling_all_shard_overrides
class TestShardedClusterScalingAddShards:
    def test_scale_shardcount(self, sc: MongoDB, custom_mdb_version: str, issuer_ca_configmap: str):
        sc["spec"]["shardCount"] = sc["spec"]["shardCount"] + 2
        sc.update()

    def test_sharded_cluster(self, sc: MongoDB):
        sc.assert_reaches_phase(Phase.Running, timeout=500)

    def test_assert_correct_automation_config_after_scaling(self, sc: MongoDB):
        logger.info("Validating automation config correctness")
        assert_correct_automation_config_after_scaling(sc)

    def test_assert_stateful_sets_after_scaling(self, sc: MongoDB):
        logger.info("Validating statefulsets in cluster(s)")
        # We added two shards, they are assigned the base distribution: [1, 2, 2]
        assert_shard_sts_members_count(sc, [[1, 2, 1], [2, 1, 2], [1, 1, 2], [1, 2, 2], [1, 2, 2]])
