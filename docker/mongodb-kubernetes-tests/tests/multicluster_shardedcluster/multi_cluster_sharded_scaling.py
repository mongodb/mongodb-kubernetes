from kubetester import (
    create_or_update_configmap,
    find_fixture,
    random_k8s_name,
    read_configmap,
    try_load,
)
from kubetester.mongodb import MongoDB
from kubetester.operator import Operator
from kubetester.phase import Phase
from pytest import fixture, mark
from tests import test_logger
from tests.conftest import (
    get_central_cluster_client,
    get_member_cluster_names,
    read_deployment_state,
)
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


# From here on, the tests are for verifying that we can change the project of the MongoDB sharded cluster resource
# and that process IDs are correctly persisted during migration scenarios.


@fixture(scope="module")
def new_project_configmap(namespace: str) -> str:
    """Create a new project configmap to simulate cluster migration scenario."""
    cm = read_configmap(namespace=namespace, name="my-project")
    project_name = f"{random_k8s_name('new-project-')}"
    return create_or_update_configmap(
        namespace=namespace,
        name=project_name,
        data={
            "baseUrl": cm["baseUrl"],
            "projectName": project_name,
            "orgId": cm["orgId"],
        },
    )


@mark.e2e_multi_cluster_sharded_process_id_persistence
class TestShardedClusterProcessIdPersistence:
    """
    Test process ID persistence during cluster migration scenarios.

    This test validates that the sharded cluster controller correctly preserves
    process IDs when changing projects (migration scenario) even with non-sequential
    member IDs in the replica sets.
    """

    def test_scale_up_first_shard(self, sc: MongoDB):
        """Scale up the first shard to create non-sequential member IDs."""
        logger.info("Scaling up first shard to create non-sequential member IDs")

        # Scale up the first shard to 3 members. This will lead to non-sequential member ids in the replicaset.
        # Similar to the multi replica set test, this creates a scenario where process IDs are not sequential
        sc["spec"]["shard"]["clusterSpecList"][0]["members"] = 3
        sc.update()

        sc.assert_reaches_phase(Phase.Running, timeout=1200)

    def test_change_project(self, sc: MongoDB, new_project_configmap: str):
        oldRsMembers = sc.get_automation_config_tester().get_replica_set_members(sc.name)

        sc["spec"]["opsManager"]["configMapRef"]["name"] = new_project_configmap
        sc.update()

        sc.assert_abandons_phase(phase=Phase.Running, timeout=1200)
        sc.assert_reaches_phase(phase=Phase.Running, timeout=1800)

        newRsMembers = sc.get_automation_config_tester().get_replica_set_members(sc.name)

        # Assert that the replica set member ids have not changed after changing the project.
        assert oldRsMembers == newRsMembers
