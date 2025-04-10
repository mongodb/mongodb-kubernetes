from kubernetes.client import ApiClient
from kubetester import try_load
from kubetester.kubetester import KubernetesTester, ensure_ent_version
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.kubetester import is_multi_cluster
from kubetester.mongodb import MongoDB, Phase
from kubetester.operator import Operator
from pytest import fixture, mark
from tests import test_logger
from tests.conftest import get_member_cluster_names
from tests.multicluster.conftest import cluster_spec_list
from tests.multicluster_shardedcluster import (
    build_expected_statefulsets,
    build_expected_statefulsets_multi,
    validate_correct_sts_in_cluster,
    validate_correct_sts_in_cluster_multi,
    validate_member_count_in_ac,
    validate_shard_configurations_in_ac,
    validate_shard_configurations_in_ac_multi,
)
from tests.shardedcluster.conftest import read_deployment_state

logger = test_logger.get_test_logger(__name__)


@fixture(scope="function")
def sc(namespace: str, custom_mdb_version: str) -> MongoDB:
    fixture_name = (
        "sharded-cluster-shard-overrides-multi-cluster.yaml"
        if is_multi_cluster()
        else "sharded-cluster-shard-overrides.yaml"
    )
    resource = MongoDB.from_yaml(
        yaml_fixture(fixture_name),
        namespace=namespace,
    )

    if try_load(resource):
        return resource

    resource.set_version(ensure_ent_version(custom_mdb_version))
    resource.set_architecture_annotation()
    return resource.update()


@mark.e2e_sharded_cluster_shard_overrides
def test_install_operator(operator: Operator):
    operator.assert_is_running()


@mark.e2e_sharded_cluster_shard_overrides
class TestShardedClusterShardOverrides:
    """
    Creates a sharded cluster configured with shard overrides. Verify deployed stateful sets and automation config.
    """

    def test_sharded_cluster_running(self, sc: MongoDB):
        sc.assert_reaches_phase(Phase.Running, timeout=1000 if is_multi_cluster() else 800)

    def test_assert_correct_automation_config(self, sc: MongoDB):
        config = KubernetesTester.get_automation_config()
        logger.info("Validating automation config correctness")
        validate_member_count_in_ac(sc, config)
        if is_multi_cluster():
            validate_shard_configurations_in_ac_multi(sc, config)
        else:
            validate_shard_configurations_in_ac(sc, config)

    def test_assert_stateful_sets(
        self, sc: MongoDB, namespace: str, central_cluster_client: ApiClient, member_cluster_clients
    ):
        logger.info("Validating statefulsets in cluster(s)")
        if is_multi_cluster():
            # We need the unique cluster index, stored in the state configmap, for computing expected sts names
            cluster_mapping = read_deployment_state(sc.name, namespace)["clusterMapping"]
            logger.debug(f"Cluster mapping in state: {cluster_mapping}")
            expected_statefulsets = build_expected_statefulsets_multi(sc, cluster_mapping)
            validate_correct_sts_in_cluster_multi(expected_statefulsets, namespace, member_cluster_clients)
        else:
            expected_statefulsets = build_expected_statefulsets(sc)
            validate_correct_sts_in_cluster(expected_statefulsets, namespace, "__default", central_cluster_client)

    def test_scale_shard_overrides(self, sc: MongoDB):
        if is_multi_cluster():
            # Override for shards 0 and 1
            sc["spec"]["shardOverrides"][0]["clusterSpecList"] = cluster_spec_list(
                get_member_cluster_names()[:2], [1, 2]
            )  # cluster2: 1->2

            # Override for shard 3
            sc["spec"]["shardOverrides"][1]["clusterSpecList"] = cluster_spec_list(
                get_member_cluster_names(), [1, 1, 3]
            )  # cluster3: 1->3

            # This replica initially had 0 votes, we need to restore the setting after using 'cluster_spec_list' above
            sc["spec"]["shardOverrides"][1]["clusterSpecList"][0]["memberConfig"] = [{"votes": 0, "priority": "0"}]

            sc.update()
            sc.assert_reaches_phase(Phase.Running, timeout=1000)
        else:
            # In the single cluster case, we first scale up, and then down
            # We cannot scale in both ways at the same time
            logger.info("Scaling up shard 3 with override")
            sc["spec"]["shardOverrides"][1]["members"] = 4  # no member count specified (2) -> 4 members (shard 3)
            sc["spec"]["shardOverrides"][1]["memberConfig"] = None
            sc.update()
            sc.assert_reaches_phase(Phase.Running, timeout=400)

            logger.info("Scaling down shards 0 and 1 with override")
            sc["spec"]["shardOverrides"][0]["members"] = 2  # Override for shards 0 and 1: 3-> 2
            sc.update()
            sc.assert_reaches_phase(Phase.Running, timeout=400)

    def test_assert_correct_automation_config_after_scaling(self, sc: MongoDB):
        resource = sc.load()
        config = KubernetesTester.get_automation_config()
        logger.info("Validating automation config correctness")
        validate_member_count_in_ac(resource, config)
        if is_multi_cluster():
            validate_shard_configurations_in_ac_multi(resource, config)
        else:
            validate_shard_configurations_in_ac(resource, config)

    def test_assert_stateful_sets_after_scaling(
        self, sc: MongoDB, namespace: str, central_cluster_client: ApiClient, member_cluster_clients
    ):
        logger.info("Validating statefulsets in cluster(s)")
        if is_multi_cluster():
            # We need the unique cluster index, stored in the state configmap, for computing expected sts names
            cluster_mapping = read_deployment_state(sc.name, namespace)["clusterMapping"]
            logger.debug(f"Cluster mapping in state: {cluster_mapping}")
            expected_statefulsets = build_expected_statefulsets_multi(sc, cluster_mapping)
            validate_correct_sts_in_cluster_multi(expected_statefulsets, namespace, member_cluster_clients)
        else:
            expected_statefulsets = build_expected_statefulsets(sc)
            validate_correct_sts_in_cluster(expected_statefulsets, namespace, "__default", central_cluster_client)
