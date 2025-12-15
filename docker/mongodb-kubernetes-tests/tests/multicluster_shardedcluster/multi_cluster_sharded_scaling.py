from kubetester import find_fixture, try_load
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

logger = test_logger.get_test_logger(__name__)


@fixture(scope="module")
def sc(namespace: str, custom_mdb_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        find_fixture("sharded-cluster-multi-cluster.yaml"),
        namespace=namespace,
        name="sh-scaling",
    )

    if try_load(resource):
        return resource

    resource.set_architecture_annotation()

    return resource


@mark.e2e_multi_cluster_sharded_scaling
class TestShardedClusterScalingInitial:

    def test_deploy_operator(self, multi_cluster_operator: Operator):
        multi_cluster_operator.assert_is_running()

    def test_create(self, sc: MongoDB, custom_mdb_version: str, issuer_ca_configmap: str):
        sc["spec"]["shard"]["clusterSpecList"] = cluster_spec_list(get_member_cluster_names(), [1, 1, 1])
        sc["spec"]["configSrv"]["clusterSpecList"] = cluster_spec_list(get_member_cluster_names(), [1, 1, 1])
        sc["spec"]["mongos"]["clusterSpecList"] = cluster_spec_list(get_member_cluster_names(), [1, 1, 1])

        sc.update()

    def test_sharded_cluster(self, sc: MongoDB):
        sc.assert_reaches_phase(Phase.Running, timeout=1200)

    def test_assert_correct_automation_config_after_scaling(self, sc: MongoDB):
        logger.info("Validating automation config correctness")
        assert_correct_automation_config_after_scaling(sc)

    def test_assert_stateful_sets_after_scaling(self, sc: MongoDB):
        logger.info("Validating statefulsets in cluster(s)")
        assert_shard_sts_members_count(sc, [[1, 1, 1]])
        assert_mongos_sts_members_count(sc, [1, 1, 1])
        assert_config_srv_sts_members_count(sc, [1, 1, 1])


@mark.e2e_multi_cluster_sharded_scaling
class TestShardedClusterScalingUpscale:

    def test_upscale(self, sc: MongoDB, custom_mdb_version: str, issuer_ca_configmap: str):
        sc["spec"]["shard"]["clusterSpecList"] = cluster_spec_list(get_member_cluster_names(), [1, 3, 1])
        sc["spec"]["configSrv"]["clusterSpecList"] = cluster_spec_list(get_member_cluster_names(), [2, 2, 1])
        sc["spec"]["mongos"]["clusterSpecList"] = cluster_spec_list(get_member_cluster_names(), [1, 1, 1])

        sc.update()

    def test_sharded_cluster(self, sc: MongoDB):
        sc.assert_reaches_phase(Phase.Running, timeout=2300)

    def test_assert_correct_automation_config_after_scaling(self, sc: MongoDB):
        logger.info("Validating automation config correctness")
        assert_correct_automation_config_after_scaling(sc)

    def test_assert_stateful_sets_after_scaling(self, sc: MongoDB):
        logger.info("Validating statefulsets in cluster(s)")
        assert_shard_sts_members_count(sc, [[1, 3, 1]])
        assert_config_srv_sts_members_count(sc, [2, 2, 1])
        assert_mongos_sts_members_count(sc, [1, 1, 1])


@mark.e2e_multi_cluster_sharded_scaling
class TestShardedClusterScalingDownscale:
    def test_downgrade_downscale(self, sc: MongoDB, custom_mdb_version: str, issuer_ca_configmap: str):
        sc["spec"]["shard"]["clusterSpecList"] = cluster_spec_list(get_member_cluster_names(), [1, 2, 1])
        sc["spec"]["configSrv"]["clusterSpecList"] = cluster_spec_list(get_member_cluster_names(), [2, 1, 1])
        sc["spec"]["mongos"]["clusterSpecList"] = cluster_spec_list(get_member_cluster_names(), [1, 1, 1])

        sc.update()

    def test_sharded_cluster(self, sc: MongoDB):
        sc.assert_reaches_phase(Phase.Running, timeout=3500)

    def test_assert_correct_automation_config_after_scaling(self, sc: MongoDB):
        logger.info("Validating automation config correctness")
        assert_correct_automation_config_after_scaling(sc)

    def test_assert_stateful_sets_after_scaling(self, sc: MongoDB):
        logger.info("Validating statefulsets in cluster(s)")
        assert_shard_sts_members_count(sc, [[1, 2, 1]])
        assert_config_srv_sts_members_count(sc, [2, 1, 1])
        assert_mongos_sts_members_count(sc, [1, 1, 1])

    def test_hosts_removed_from_monitoring_after_scaling(self, sc: MongoDB):
        """Verifies that scaled-down hosts are removed from OM monitoring."""
        # After downscale: 4 shard + 4 config + 3 mongos = 11 hosts
        sc.get_om_tester().wait_until_hosts_count(11, timeout=60)


@mark.e2e_multi_cluster_sharded_scaling
class TestShardedClusterScalingDownscaleToZero:
    def test_downscale_to_zero(self, sc: MongoDB, custom_mdb_version: str, issuer_ca_configmap: str):
        sc["spec"]["shard"]["clusterSpecList"] = cluster_spec_list(get_member_cluster_names(), [0, 2, 1])
        sc["spec"]["configSrv"]["clusterSpecList"] = cluster_spec_list(get_member_cluster_names(), [1, 1, 1])
        sc["spec"]["mongos"]["clusterSpecList"] = cluster_spec_list(get_member_cluster_names(), [1, 0, 0])

        sc.update()

    def test_sharded_cluster(self, sc: MongoDB):
        sc.assert_reaches_phase(Phase.Running, timeout=3500)

    def test_assert_correct_automation_config_after_scaling(self, sc: MongoDB):
        logger.info("Validating automation config correctness")
        assert_correct_automation_config_after_scaling(sc)

    def test_assert_stateful_sets_after_scaling(self, sc: MongoDB):
        logger.info("Validating statefulsets in cluster(s)")
        assert_shard_sts_members_count(sc, [[0, 2, 1]])
        assert_config_srv_sts_members_count(sc, [1, 1, 1])
        assert_mongos_sts_members_count(sc, [1, 0, 0])
