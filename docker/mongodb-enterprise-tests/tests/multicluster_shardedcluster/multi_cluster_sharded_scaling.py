from typing import Dict, List

import kubernetes
import pytest
from kubetester import find_fixture, try_load
from kubetester.kubetester import KubernetesTester
from kubetester.mongodb import MongoDB, Phase
from kubetester.mongodb_multi import MultiClusterClient
from kubetester.operator import Operator
from pytest import fixture, mark
from tests import test_logger
from tests.conftest import get_member_cluster_names
from tests.multicluster.conftest import cluster_spec_list
from tests.multicluster_shardedcluster import (
    assert_config_srv_sts_members_count,
    assert_correct_automation_config_after_scaling,
    assert_mongos_sts_members_count,
    assert_shard_sts_members_count,
    validate_member_count_in_ac,
    validate_shard_configurations_in_ac_multi,
)
from tests.shardedcluster.conftest import (
    get_member_cluster_clients_using_cluster_mapping,
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
class TestShardedClusterScalingUpgradeByOne:

    def test_upgrade_by_one(self, sc: MongoDB, custom_mdb_version: str, issuer_ca_configmap: str):
        sc["spec"]["shard"]["clusterSpecList"] = cluster_spec_list(get_member_cluster_names(), [1, 2, 1])
        sc["spec"]["configSrv"]["clusterSpecList"] = cluster_spec_list(get_member_cluster_names(), [2, 1, 1])
        sc["spec"]["mongos"]["clusterSpecList"] = cluster_spec_list(get_member_cluster_names(), [1, 1, 2])

        sc.update()

    def test_sharded_cluster(self, sc: MongoDB):
        sc.assert_reaches_phase(Phase.Running, timeout=2300)

    def test_assert_correct_automation_config_after_scaling(self, sc: MongoDB):
        logger.info("Validating automation config correctness")
        assert_correct_automation_config_after_scaling(sc)

    def test_assert_stateful_sets_after_scaling(self, sc: MongoDB):
        logger.info("Validating statefulsets in cluster(s)")
        assert_shard_sts_members_count(sc, [[1, 2, 1]])
        assert_config_srv_sts_members_count(sc, [2, 1, 1])
        assert_mongos_sts_members_count(sc, [1, 1, 2])


@mark.e2e_multi_cluster_sharded_scaling
class TestShardedClusterScalingDowngradeUpgradeByOne:

    def test_downgrade_upgrade_by_one(self, sc: MongoDB, custom_mdb_version: str, issuer_ca_configmap: str):
        sc["spec"]["shard"]["clusterSpecList"] = cluster_spec_list(get_member_cluster_names(), [2, 1, 1])
        sc["spec"]["configSrv"]["clusterSpecList"] = cluster_spec_list(get_member_cluster_names(), [1, 1, 2])
        sc["spec"]["mongos"]["clusterSpecList"] = cluster_spec_list(get_member_cluster_names(), [1, 2, 1])

        sc.update()

    def test_sharded_cluster(self, sc: MongoDB):
        sc.assert_reaches_phase(Phase.Running, timeout=3500)

    def test_assert_correct_automation_config_after_scaling(self, sc: MongoDB):
        logger.info("Validating automation config correctness")
        assert_correct_automation_config_after_scaling(sc)

    def test_assert_stateful_sets_after_scaling(self, sc: MongoDB):
        logger.info("Validating statefulsets in cluster(s)")
        assert_shard_sts_members_count(sc, [[2, 1, 1]])
        assert_config_srv_sts_members_count(sc, [1, 1, 2])
        assert_mongos_sts_members_count(sc, [1, 2, 1])


@mark.e2e_multi_cluster_sharded_scaling
class TestShardedClusterScalingDowngradeUpgradeToZero:

    def test_downgrade_upgrade_to_zero(self, sc: MongoDB, custom_mdb_version: str, issuer_ca_configmap: str):
        sc["spec"]["shard"]["clusterSpecList"] = cluster_spec_list(get_member_cluster_names(), [0, 2, 1])
        sc["spec"]["configSrv"]["clusterSpecList"] = cluster_spec_list(get_member_cluster_names(), [2, 0, 1])
        sc["spec"]["mongos"]["clusterSpecList"] = cluster_spec_list(get_member_cluster_names(), [1, 0, 2])

        sc.update()

    def test_sharded_cluster(self, sc: MongoDB):
        sc.assert_reaches_phase(Phase.Running, timeout=3500)

    def test_assert_correct_automation_config_after_scaling(self, sc: MongoDB):
        logger.info("Validating automation config correctness")
        assert_correct_automation_config_after_scaling(sc)

    def test_assert_stateful_sets_after_scaling(self, sc: MongoDB):
        logger.info("Validating statefulsets in cluster(s)")
        assert_shard_sts_members_count(sc, [[0, 2, 1]])
        assert_config_srv_sts_members_count(sc, [2, 0, 1])
        assert_mongos_sts_members_count(sc, [1, 0, 2])
