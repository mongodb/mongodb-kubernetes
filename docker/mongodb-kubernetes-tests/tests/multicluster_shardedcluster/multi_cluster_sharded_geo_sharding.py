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
    assert_config_srv_sts_members_count,
    assert_correct_automation_config_after_scaling,
    assert_mongos_sts_members_count,
    assert_shard_sts_members_count,
)
from tests.shardedcluster.conftest import get_member_cluster_clients_using_cluster_mapping

MDB_RESOURCE_NAME = "sh-geo-sharding"
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


@mark.e2e_multi_cluster_sharded_geo_sharding
class TestShardedClusterGeoSharding:
    """This test verifies scenario where user wants to have distributed local writes -> https://www.mongodb.com/docs/manual/tutorial/sharding-high-availability-writes/.
    Assuming that writes to shard-x happen closer to cluster-y we can have primary for shard-x set in cluster-y.
    We do this by setting high priority for particular replica members to elect primary (write) replicas in
    desired clusters."""

    def test_deploy_operator(self, multi_cluster_operator: Operator):
        multi_cluster_operator.assert_is_running()

    def test_create_primary_for_each_shard_in_different_cluster(
        self, sc: MongoDB, custom_mdb_version: str, issuer_ca_configmap: str
    ):
        sc["spec"]["shardCount"] = 3
        # The distribution below is the default one but won't be used as all shards are overridden
        sc["spec"]["shard"]["clusterSpecList"] = cluster_spec_list(get_member_cluster_names(), [1, 1, 1])
        sc["spec"]["configSrv"]["clusterSpecList"] = cluster_spec_list(get_member_cluster_names(), [1, 1, 1])
        sc["spec"]["mongos"]["clusterSpecList"] = cluster_spec_list(get_member_cluster_names(), [1, 1, 1])

        # All shards contain overrides
        sc["spec"]["shardOverrides"] = [
            {
                "shardNames": [f"{sc.name}-0"],
                "clusterSpecList": cluster_spec_list(
                    get_member_cluster_names(),
                    [1, 1, 1],
                    [
                        [
                            {
                                "priority": "1",
                            }
                        ],
                        [
                            {
                                "priority": "1",
                            }
                        ],
                        [
                            {
                                "priority": "100",
                            }
                        ],
                    ],
                ),
            },
            {
                "shardNames": [f"{sc.name}-1"],
                "clusterSpecList": cluster_spec_list(
                    get_member_cluster_names(),
                    [1, 1, 1],
                    [
                        [
                            {
                                "priority": "1",
                            }
                        ],
                        [
                            {
                                "priority": "100",
                            }
                        ],
                        [
                            {
                                "priority": "1",
                            }
                        ],
                    ],
                ),
            },
            {
                "shardNames": [f"{sc.name}-2"],
                "clusterSpecList": cluster_spec_list(
                    get_member_cluster_names(),
                    [1, 1, 1],
                    [
                        [
                            {
                                "priority": "100",
                            }
                        ],
                        [
                            {
                                "priority": "1",
                            }
                        ],
                        [
                            {
                                "priority": "1",
                            }
                        ],
                    ],
                ),
            },
        ]

        sc.update()

    def test_sharded_cluster(self, sc: MongoDB):
        sc.assert_reaches_phase(Phase.Running, timeout=1200)

    def test_assert_correct_automation_config(self, sc: MongoDB):
        logger.info("Validating automation config correctness")
        assert_correct_automation_config_after_scaling(sc)

    def test_assert_stateful_sets(self, sc: MongoDB):
        logger.info("Validating statefulsets in cluster(s)")
        assert_shard_sts_members_count(sc, [[1, 1, 1], [1, 1, 1], [1, 1, 1]])
        assert_mongos_sts_members_count(sc, [1, 1, 1])
        assert_config_srv_sts_members_count(sc, [1, 1, 1])

    def test_assert_shard_primary_replicas(self, sc: MongoDB):
        logger.info("Validating shard primaries in cluster(s)")
        cluster_primary_member_mapping = {
            0: 2,  # -> shard_idx: 0, cluster_idx: 2
            1: 1,  # -> shard_idx: 1, cluster_idx: 1
            2: 0,  # -> shard_idx: 2, cluster_idx: 0
        }
        for shard_idx, cluster_idx in cluster_primary_member_mapping.items():
            shard_primary_hostname = sc.shard_hostname(shard_idx, 0, cluster_idx)
            client = KubernetesTester.check_hosts_are_ready(hosts=[shard_primary_hostname])
            assert client.is_primary
