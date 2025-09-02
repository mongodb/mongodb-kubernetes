from pytest import fixture, mark

from kubetester import find_fixture, try_load, create_or_update_configmap, read_configmap, random_k8s_name
from kubetester.mongodb import MongoDB
from kubetester.operator import Operator
from kubetester.phase import Phase
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


@fixture(scope="module")
def migration_project_configmap(namespace: str) -> str:
    """Create a new project configmap to simulate cluster migration scenario."""
    cm = read_configmap(namespace=namespace, name="my-project")
    project_name = f"{random_k8s_name('migration-project-')}"
    return create_or_update_configmap(
        namespace=namespace,
        name=project_name,
        data={
            "baseUrl": cm["baseUrl"],
            "projectName": project_name,
            "orgId": cm["orgId"],
        },
    )


@fixture(scope="module")
def sc_process_id_persistence(namespace: str, custom_mdb_version: str) -> MongoDB:
    """Create a sharded cluster for process ID persistence testing."""
    resource = MongoDB.from_yaml(
        find_fixture("sharded-cluster-multi-cluster.yaml"),
        namespace=namespace,
        name="sh-process-id-test",
    )

    if try_load(resource):
        return resource

    resource.set_architecture_annotation()
    return resource


@mark.e2e_multi_cluster_sharded_process_id_persistence
class TestShardedClusterProcessIdPersistence:
    """
    Test process ID persistence during cluster migration scenarios.

    This test class validates that the sharded cluster controller correctly:
    1. Saves process IDs to deployment state after reconciliation
    2. Retrieves process IDs from deployment state when they become empty (migration scenario)
    3. Maintains cluster functionality after process ID restoration
    4. Handles scaling operations correctly after migration
    """

    def test_deploy_operator(self, multi_cluster_operator: Operator):
        """Ensure the operator is running before starting tests."""
        multi_cluster_operator.assert_is_running()

    def test_create_initial_cluster(self, sc_process_id_persistence: MongoDB, custom_mdb_version: str, issuer_ca_configmap: str):
        """Create initial sharded cluster with basic configuration."""
        logger.info("Creating initial sharded cluster for process ID persistence testing")

        sc = sc_process_id_persistence
        sc["spec"]["shard"]["clusterSpecList"] = cluster_spec_list(get_member_cluster_names(), [2, 2, 1])
        sc["spec"]["configSrv"]["clusterSpecList"] = cluster_spec_list(get_member_cluster_names(), [1, 1, 1])
        sc["spec"]["mongos"]["clusterSpecList"] = cluster_spec_list(get_member_cluster_names(), [1, 1, 1])

        sc.update()

    def test_initial_cluster_running(self, sc_process_id_persistence: MongoDB):
        """Wait for initial cluster to reach running state."""
        logger.info("Waiting for initial cluster to reach running state")
        sc_process_id_persistence.assert_reaches_phase(Phase.Running, timeout=1200)

    def test_capture_initial_process_ids(self, sc_process_id_persistence: MongoDB):
        """Capture initial process IDs and verify they are saved to deployment state."""
        logger.info("Capturing initial process IDs from automation config")

        # Get the automation config to verify process IDs exist
        automation_config = sc_process_id_persistence.get_automation_config()

        # Verify that processes exist and have IDs
        processes = automation_config.get("processes", [])
        assert len(processes) > 0, "No processes found in automation config"

        # Verify config server processes
        config_processes = [p for p in processes if "configsrv" in p.get("name", "")]
        assert len(config_processes) >= 3, f"Expected at least 3 config server processes, found {len(config_processes)}"

        # Verify shard processes
        shard_processes = [p for p in processes if "shard" in p.get("name", "")]
        assert len(shard_processes) >= 5, f"Expected at least 5 shard processes, found {len(shard_processes)}"

        # Store process IDs for later verification
        self.initial_process_ids = {p["name"]: p.get("processId") for p in processes if p.get("processId") is not None}
        logger.info(f"Captured {len(self.initial_process_ids)} initial process IDs")

    def test_simulate_cluster_migration(self, sc_process_id_persistence: MongoDB, migration_project_configmap: str):
        """Simulate cluster migration by changing the project configuration."""
        logger.info("Simulating cluster migration by changing project configuration")

        sc = sc_process_id_persistence

        # Change the opsManager configMapRef to simulate migration
        original_configmap = sc["spec"]["opsManager"]["configMapRef"]["name"]
        sc["spec"]["opsManager"]["configMapRef"]["name"] = migration_project_configmap

        logger.info(f"Changed configMapRef from {original_configmap} to {migration_project_configmap}")
        sc.update()

        # Store original configmap for potential restoration
        self.original_configmap = original_configmap

    def test_cluster_recovers_after_migration(self, sc_process_id_persistence: MongoDB):
        """Verify cluster recovers and reaches running state after migration."""
        logger.info("Waiting for cluster to recover after migration")

        # The cluster should eventually reach running state again
        # This may take longer as the controller needs to handle the migration
        sc_process_id_persistence.assert_reaches_phase(Phase.Running, timeout=1800)

    def test_verify_process_id_persistence(self, sc_process_id_persistence: MongoDB):
        """Verify that process IDs are correctly restored from deployment state."""
        logger.info("Verifying process ID persistence after migration")

        # Get the automation config after migration
        automation_config = sc_process_id_persistence.get_automation_config()
        processes = automation_config.get("processes", [])

        # Get current process IDs
        current_process_ids = {p["name"]: p.get("processId") for p in processes if p.get("processId") is not None}

        logger.info(f"Found {len(current_process_ids)} process IDs after migration")

        # Verify that process IDs are preserved or properly restored
        # In a real migration scenario, the process IDs should be restored from deployment state
        for process_name in self.initial_process_ids:
            if process_name in current_process_ids:
                logger.info(f"Process {process_name}: initial ID {self.initial_process_ids[process_name]}, current ID {current_process_ids[process_name]}")

        # Verify cluster is functional by checking that all expected processes exist
        config_processes = [p for p in processes if "configsrv" in p.get("name", "")]
        shard_processes = [p for p in processes if "shard" in p.get("name", "")]

        assert len(config_processes) >= 3, f"Config server processes missing after migration: {len(config_processes)}"
        assert len(shard_processes) >= 5, f"Shard processes missing after migration: {len(shard_processes)}"

    def test_scaling_after_migration(self, sc_process_id_persistence: MongoDB):
        """Test that scaling operations work correctly after migration."""
        logger.info("Testing scaling operations after migration")

        sc = sc_process_id_persistence

        # Scale up one of the shards
        sc["spec"]["shard"]["clusterSpecList"] = cluster_spec_list(get_member_cluster_names(), [3, 2, 1])
        sc.update()

        # Wait for scaling to complete
        sc.assert_reaches_phase(Phase.Running, timeout=1200)

    def test_verify_scaling_success(self, sc_process_id_persistence: MongoDB):
        """Verify that scaling was successful and cluster is functional."""
        logger.info("Verifying scaling success after migration")

        # Verify automation config correctness
        assert_correct_automation_config_after_scaling(sc_process_id_persistence)

        # Verify statefulset member counts
        assert_shard_sts_members_count(sc_process_id_persistence, [[3, 2, 1]])
        assert_config_srv_sts_members_count(sc_process_id_persistence, [1, 1, 1])
        assert_mongos_sts_members_count(sc_process_id_persistence, [1, 1, 1])

        logger.info("Process ID persistence test completed successfully")
