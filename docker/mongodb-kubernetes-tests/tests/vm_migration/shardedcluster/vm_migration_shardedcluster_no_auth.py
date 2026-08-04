"""
VM migration for a sharded cluster with authentication disabled.

Exercises the import tool (kubectl-mongodb migrate-to-mck) end to end:
deploys pseudo-VM StatefulSets, configures the automation config, runs the
generate step, applies the generated CR, and validates dry-run. The cluster
is imported VM-only (all Kubernetes node counts at 0) and then grown
incrementally, component by component (config server, shard, mongos), one
member at a time — asserting the Migrating condition reasons Extending,
Pruning, InProgress, and MigrationComplete throughout, alongside data
continuity and process names.

All three VM name overrides are exercised:
  configServerNameOverride: VM config RS name "vm-config" differs from the K8s default.
  shardNameOverrides: VM shard RS name "vm-shard-0" differs from the K8s default.
  shardedClusterNameOverride: VM mongos name "vm-mongos" differs from the K8s default.
"""

from kubetester import get_statefulset
from kubetester.kubetester import KubernetesTester, ensure_ent_version, skip_if_local
from kubetester.mongodb import MongoDB
from kubetester.mongotester import MongoDBBackgroundTester
from kubetester.omtester import OMContext, OMTester
from kubetester.operator import Operator
from kubetester.phase import Phase
from pytest import fixture, mark
from tests.vm_migration.vm_migration_common_helper import (
    assert_migration_data_exists,
    generated_mongodb_doc,
    generated_user_docs,
    insert_migration_data,
    run_generate_cr,
)
from tests.vm_migration.vm_migration_dry_run import run_migration_dry_run_connectivity_passes
from tests.vm_migration.vm_migration_sharded_helper import (
    MIN_VM_CONFIGSRV,
    MIN_VM_MONGOS,
    MIN_VM_SHARD,
    apply_generated_sharded_cluster_resource,
    assert_common_generated_sharded_cr_shape,
    assert_connection_string_after_full_sharded_migration,
    assert_k8s_sharded_process_names,
    build_sharded_cluster_ac,
    deploy_vm_sharded_configsrv_service,
    deploy_vm_sharded_configsrv_statefulset,
    deploy_vm_sharded_mongos_service,
    deploy_vm_sharded_mongos_statefulset,
    deploy_vm_sharded_shard_service,
    deploy_vm_sharded_shard_statefulset,
    extend_and_prune_sharded_mongos,
    extend_and_prune_sharded_replica_components,
    sharded_connection_string_tester,
    vm_mongos_tester,
)

CONFIGSRV_STS_NAME = "vm-sharded-configsrv"
SHARD_STS_NAME = "vm-sharded-shard"
MONGOS_STS_NAME = "vm-sharded-mongos"
CONFIGSRV_SVC_NAME = "vm-sharded-configsrv"
SHARD_SVC_NAME = "vm-sharded-shard"
MONGOS_SVC_NAME = "vm-sharded-mongos"
MDB_RESOURCE_NAME = "sharded-migration"
VM_CONFIG_RS_NAME = "vm-config"
VM_SHARD_RS_NAME = "vm-shard-0"
VM_MONGOS_NAME = "vm-mongos"


@fixture(scope="module")
def om_tester(namespace: str) -> OMTester:
    config_map = KubernetesTester.read_configmap(namespace, "my-project")
    secret = KubernetesTester.read_secret(namespace, "my-credentials")
    tester = OMTester(OMContext.build_from_config_map_and_secret(config_map, secret))
    tester.ensure_agent_api_key()
    return tester


@fixture(scope="module")
def vm_sharded_configsrv_sts(namespace: str, om_tester: OMTester):
    return deploy_vm_sharded_configsrv_statefulset(namespace, om_tester)


@fixture(scope="module")
def vm_sharded_shard_sts(namespace: str, om_tester: OMTester):
    return deploy_vm_sharded_shard_statefulset(namespace, om_tester)


@fixture(scope="module")
def vm_sharded_mongos_sts(namespace: str, om_tester: OMTester):
    return deploy_vm_sharded_mongos_statefulset(namespace, om_tester)


@fixture(scope="module")
def vm_sharded_configsrv_service(namespace: str):
    return deploy_vm_sharded_configsrv_service(namespace)


@fixture(scope="module")
def vm_sharded_shard_service(namespace: str):
    return deploy_vm_sharded_shard_service(namespace)


@fixture(scope="module")
def vm_sharded_mongos_service(namespace: str):
    return deploy_vm_sharded_mongos_service(namespace)


@fixture(scope="module")
def generated_cr_yaml(namespace: str) -> str:
    return run_generate_cr(namespace, resource_name_override=MDB_RESOURCE_NAME)


@fixture(scope="module")
def generated_cr(generated_cr_yaml: str) -> dict:
    return generated_mongodb_doc(generated_cr_yaml)


@fixture(scope="module")
def mdb_migration(namespace: str, generated_cr_yaml: str) -> MongoDB:
    # Start VM-only (all K8s node counts == 0) so this scenario exercises the incremental extend flow:
    # K8s members are grown one at a time across config server, shard, and mongos, asserting the
    # Migrating reasons Extending/Pruning/InProgress/MigrationComplete.
    return apply_generated_sharded_cluster_resource(
        namespace,
        generated_cr_yaml,
        config_rs_name=VM_CONFIG_RS_NAME,
        incremental=True,
    )


@fixture(scope="module")
def mdb_health_checker(mdb_migration: MongoDB) -> MongoDBBackgroundTester:
    # Seed from the connection-string secret so the checker tracks the current active mongos (VM
    # mongos while config/shard migrate, since mongos migrate last). allowed_sequential_failures is
    # raised above the default of 1 so a brief reconfig blip during a promote/prune step does not fail
    # the health assertion. The checker is stopped before the mongos cutover (which prunes the VM
    # mongos it is seeded with); the mongos phase asserts connectivity via fresh secret reads instead.
    return MongoDBBackgroundTester(
        sharded_connection_string_tester(mdb_migration),
        allowed_sequential_failures=3,
    )


@mark.e2e_vm_migration_shardedcluster_no_auth
def test_deploy_vm_sharded(
    namespace: str,
    vm_sharded_configsrv_sts,
    vm_sharded_shard_sts,
    vm_sharded_mongos_sts,
    vm_sharded_configsrv_service,
    vm_sharded_shard_service,
    vm_sharded_mongos_service,
):
    def configsrv_sts_is_ready():
        sts = get_statefulset(namespace, vm_sharded_configsrv_sts["metadata"]["name"])
        return sts.status.ready_replicas == vm_sharded_configsrv_sts["spec"]["replicas"]

    def shard_sts_is_ready():
        sts = get_statefulset(namespace, vm_sharded_shard_sts["metadata"]["name"])
        return sts.status.ready_replicas == vm_sharded_shard_sts["spec"]["replicas"]

    def mongos_sts_is_ready():
        sts = get_statefulset(namespace, vm_sharded_mongos_sts["metadata"]["name"])
        return sts.status.ready_replicas == vm_sharded_mongos_sts["spec"]["replicas"]

    KubernetesTester.wait_until(configsrv_sts_is_ready, timeout=300)
    KubernetesTester.wait_until(shard_sts_is_ready, timeout=300)
    KubernetesTester.wait_until(mongos_sts_is_ready, timeout=300)


@mark.e2e_vm_migration_shardedcluster_no_auth
def test_configure_ac(
    namespace: str,
    om_tester: OMTester,
    vm_sharded_configsrv_sts,
    vm_sharded_shard_sts,
    vm_sharded_mongos_sts,
    vm_sharded_configsrv_service,
    vm_sharded_shard_service,
    vm_sharded_mongos_service,
    custom_mdb_version: str,
):
    mdb_version = ensure_ent_version(custom_mdb_version)
    ac = om_tester.api_get_automation_config()
    if len(ac.get("processes", [])) > 0:
        return

    ac = build_sharded_cluster_ac(
        om_tester,
        configsrv_sts_name=CONFIGSRV_STS_NAME,
        shard_sts_name=SHARD_STS_NAME,
        mongos_sts_name=MONGOS_STS_NAME,
        configsrv_service_name=CONFIGSRV_SVC_NAME,
        shard_service_name=SHARD_SVC_NAME,
        mongos_service_name=MONGOS_SVC_NAME,
        namespace=namespace,
        mongodb_version=mdb_version,
        config_rs_name=VM_CONFIG_RS_NAME,
        shard_rs_name=VM_SHARD_RS_NAME,
        config_server_count=MIN_VM_CONFIGSRV,
        shard_count=MIN_VM_SHARD,
        mongos_count=MIN_VM_MONGOS,
        cluster_name=VM_MONGOS_NAME,
        compressors="snappy,zstd",
        directory_per_db=True,
    )
    om_tester.api_put_automation_config(ac)
    om_tester.wait_agents_ready(timeout=600)


@mark.e2e_vm_migration_shardedcluster_no_auth
@skip_if_local()
def test_connectivity_before_migration(namespace: str):
    """Sharded cluster is reachable without authentication before migration."""
    vm_mongos_tester(MONGOS_STS_NAME, MONGOS_SVC_NAME, namespace).assert_connectivity()


@mark.e2e_vm_migration_shardedcluster_no_auth
def test_install_operator(operator: Operator):
    operator.assert_is_running()


@mark.e2e_vm_migration_shardedcluster_no_auth
def test_insert_migration_data(namespace: str):
    insert_migration_data(vm_mongos_tester(MONGOS_STS_NAME, MONGOS_SVC_NAME, namespace))


@mark.e2e_vm_migration_shardedcluster_no_auth
def test_common_generated_cr_shape(generated_cr: dict, version_id: str):
    assert_common_generated_sharded_cr_shape(
        generated_cr,
        expected_config_count=MIN_VM_CONFIGSRV,
        expected_shard_count=MIN_VM_SHARD,
        expected_mongos_count=MIN_VM_MONGOS,
        version_id=version_id,
    )


@mark.e2e_vm_migration_shardedcluster_no_auth
def test_no_security_in_cr(generated_cr: dict):
    """Auth is disabled — the generated CR must not contain a security section."""
    spec = generated_cr.get("spec", {})
    assert (
        "security" not in spec
    ), f"Expected no security section for auth-disabled deployment, got: {spec.get('security')}"


@mark.e2e_vm_migration_shardedcluster_no_auth
def test_no_user_crs_emitted(generated_cr_yaml: str):
    """Without auth, migrate must not produce any MongoDBUser documents."""
    user_docs = generated_user_docs(generated_cr_yaml)
    assert len(user_docs) == 0, f"Expected 0 user CRs, got {len(user_docs)}"


@mark.e2e_vm_migration_shardedcluster_no_auth
def test_additional_mongod_config(generated_cr: dict):
    """additionalMongodConfig must reflect the net.compression.compressors and storage settings in each component."""
    for component in ("configSrv", "shard"):
        amc = generated_cr["spec"].get(component, {}).get("additionalMongodConfig", {})
        assert (
            amc.get("net", {}).get("compression", {}).get("compressors") == "snappy,zstd"
        ), f"Expected compressors 'snappy,zstd' in {component}, got: {amc}"
        assert (
            amc.get("storage", {}).get("directoryPerDB") is True
        ), f"Expected directoryPerDB=true in {component}, got: {amc}"


@mark.e2e_vm_migration_shardedcluster_no_auth
def test_version_set(generated_cr: dict, custom_mdb_version: str):
    """spec.version must match the MongoDB version used in the AC."""
    assert generated_cr["spec"]["version"] == ensure_ent_version(custom_mdb_version)


@mark.e2e_vm_migration_shardedcluster_no_auth
def test_agent_config(generated_cr: dict):
    """Agent config must include logRotate and systemLog from the process config."""
    agent = generated_cr["spec"].get("agent", {}).get("mongod", {})
    lr = agent.get("logRotate", {})
    assert (
        lr.get("sizeThresholdMB") == "1000" or lr.get("sizeThresholdMB") == 1000
    ), f"Expected logRotate.sizeThresholdMB=1000, got: {lr}"
    sl = agent.get("systemLog", {})
    assert sl.get("destination") == "file", f"Expected systemLog.destination=file, got: {sl}"
    assert sl.get("path") == "/data/mongodb.log", f"Expected systemLog.path, got: {sl}"


@mark.e2e_vm_migration_shardedcluster_no_auth
def test_vm_deployment_automation_config(om_tester: OMTester):
    ac_tester = om_tester.get_automation_config_tester()
    vm_total = MIN_VM_CONFIGSRV + MIN_VM_SHARD + MIN_VM_MONGOS
    assert len(ac_tester.get_all_processes()) == vm_total
    assert len(ac_tester.get_monitoring_versions()) == vm_total
    assert len(ac_tester.get_backup_versions()) == vm_total
    assert len(ac_tester.get_sharding_entries()) == 1


@mark.e2e_vm_migration_shardedcluster_no_auth
def test_migration_dry_run_connectivity_passes(mdb_migration: MongoDB):
    run_migration_dry_run_connectivity_passes(mdb_migration)


@mark.e2e_vm_migration_shardedcluster_no_auth
def test_migrate_vm_to_kubernetes(mdb_migration: MongoDB):
    # VM-only: all K8s counts are 0 and the cluster serves entirely from the VM externalMembers.
    mdb_migration.assert_reaches_phase(Phase.Running, timeout=1800)
    assert mdb_migration["spec"]["configServerCount"] == 0
    assert mdb_migration["spec"]["mongodsPerShardCount"] == 0
    assert mdb_migration["spec"]["mongosCount"] == 0


# Note: the max-voting-members validation is covered by the other sharded scenarios (which pre-provision
# K8s members and flip their votes). It is intentionally omitted here: this scenario starts VM-only and
# grows members incrementally, and the operator forbids removing Kubernetes members during migration, so
# the "scale up to trip the limit, then scale back down" approach cannot recover to a valid state.


@mark.e2e_vm_migration_shardedcluster_no_auth
@skip_if_local()
def test_connectivity_after_migration(mdb_migration: MongoDB):
    """Sharded cluster remains reachable (via the VM mongos) after the VM-only import."""
    sharded_connection_string_tester(mdb_migration).assert_connectivity()


@mark.e2e_vm_migration_shardedcluster_no_auth
def test_migration_data_exists_after_migration(mdb_migration: MongoDB):
    assert_migration_data_exists(sharded_connection_string_tester(mdb_migration))


@mark.e2e_vm_migration_shardedcluster_no_auth
@skip_if_local()
def test_start_background_health_checker(mdb_health_checker: MongoDBBackgroundTester):
    mdb_health_checker.start()


@mark.e2e_vm_migration_shardedcluster_no_auth
def test_incremental_extend_config_and_shard(mdb_migration: MongoDB, om_tester: OMTester):
    total_vms = MIN_VM_CONFIGSRV + MIN_VM_SHARD + MIN_VM_MONGOS
    extend_and_prune_sharded_replica_components(
        mdb_migration,
        om_tester,
        config_rs_name=VM_CONFIG_RS_NAME,
        shard_rs_name=VM_SHARD_RS_NAME,
        mongos_cluster_name=VM_MONGOS_NAME,
        total_vms=total_vms,
    )


@mark.e2e_vm_migration_shardedcluster_no_auth
@skip_if_local()
def test_mongodb_reachable_during_config_and_shard(mdb_health_checker: MongoDBBackgroundTester):
    # VM mongos are still up through the config-server and shard cutovers, so the secret-seeded checker
    # is valid here. Stop it before the mongos phase prunes the VM mongos it was seeded with.
    mdb_health_checker.assert_healthiness()
    mdb_health_checker.stop()


@mark.e2e_vm_migration_shardedcluster_no_auth
def test_incremental_prune_mongos(mdb_migration: MongoDB, om_tester: OMTester):
    total_vms = MIN_VM_CONFIGSRV + MIN_VM_SHARD + MIN_VM_MONGOS
    pruned_so_far = MIN_VM_CONFIGSRV + MIN_VM_SHARD
    extend_and_prune_sharded_mongos(
        mdb_migration,
        om_tester,
        mongos_cluster_name=VM_MONGOS_NAME,
        total_vms=total_vms,
        pruned_so_far=pruned_so_far,
    )


@mark.e2e_vm_migration_shardedcluster_no_auth
def test_connection_string_after_full_migration(mdb_migration: MongoDB):
    assert_connection_string_after_full_sharded_migration(mdb_migration)


@mark.e2e_vm_migration_shardedcluster_no_auth
def test_process_names(om_tester: OMTester, mdb_migration: MongoDB):
    assert_k8s_sharded_process_names(om_tester, mdb_migration)


@mark.e2e_vm_migration_shardedcluster_no_auth
@skip_if_local()
def test_connectivity_after_promote(mdb_migration: MongoDB):
    """Sharded cluster remains reachable without authentication after promote and prune."""
    mdb_migration.tester().assert_connectivity()


@mark.e2e_vm_migration_shardedcluster_no_auth
def test_migration_data_exists_after_promote(mdb_migration: MongoDB):
    assert_migration_data_exists(mdb_migration.tester())
