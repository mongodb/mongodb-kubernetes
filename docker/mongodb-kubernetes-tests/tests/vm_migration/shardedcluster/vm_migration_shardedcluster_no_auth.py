"""
VM migration for a sharded cluster with authentication disabled.

Exercises the import tool (kubectl-mongodb migrate-to-mck) end to end:
deploys pseudo-VM StatefulSets, configures the automation config, runs the
generate step, applies the generated CR, validates dry-run, promotes and
prunes each component (config server, shard, mongos), and asserts data
continuity and process names throughout.

All three VM name overrides are exercised:
  configServerNameOverride: VM config RS name "vm-config" differs from the K8s default.
  shardNameOverrides: VM shard RS name "vm-shard-0" differs from the K8s default.
  shardedClusterNameOverride: VM mongos name "vm-mongos" differs from the K8s default.
"""

from kubetester import get_statefulset, try_load
from kubetester.kubetester import KubernetesTester, ensure_ent_version, skip_if_local
from kubetester.mongodb import MongoDB
from kubetester.mongotester import MongoDBBackgroundTester
from kubetester.omtester import OMContext, OMTester
from kubetester.operator import Operator
from kubetester.phase import Phase
from pytest import fixture, mark
from tests.vm_migration.vm_migration_dry_run import run_migration_dry_run_connectivity_passes
from tests.vm_migration.vm_migration_helpers import (
    apply_generated_sharded_cluster_resource,
    assert_common_generated_sharded_cr_shape,
    assert_connection_string_after_full_sharded_migration,
    assert_k8s_sharded_process_names,
    assert_migration_data_exists,
    build_sharded_cluster_ac,
    deploy_vm_sharded_mongod_statefulset,
    deploy_vm_sharded_mongos_service,
    deploy_vm_sharded_mongos_statefulset,
    deploy_vm_sharded_service,
    generated_mongodb_doc,
    generated_user_docs,
    insert_migration_data,
    run_generate_cr,
    vm_mongos_tester,
)

MONGOD_STS_NAME = "vm-sharded-mongod"
MONGOS_STS_NAME = "vm-sharded-mongos"
MONGOD_SVC_NAME = "vm-sharded-mongod"
MONGOS_SVC_NAME = "vm-sharded-mongos"
CONFIG_SERVER_COUNT = 3
SHARD_COUNT = 3
MONGOS_COUNT = 2
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
def vm_sharded_mongod_sts(namespace: str, om_tester: OMTester):
    return deploy_vm_sharded_mongod_statefulset(namespace, om_tester)


@fixture(scope="module")
def vm_sharded_mongos_sts(namespace: str, om_tester: OMTester):
    return deploy_vm_sharded_mongos_statefulset(namespace, om_tester)


@fixture(scope="module")
def vm_sharded_service(namespace: str):
    return deploy_vm_sharded_service(namespace)


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
    return apply_generated_sharded_cluster_resource(
        namespace,
        generated_cr_yaml,
        config_rs_name=VM_CONFIG_RS_NAME,
    )


@fixture(scope="module")
def mdb_health_checker(mdb_migration: MongoDB) -> MongoDBBackgroundTester:
    return MongoDBBackgroundTester(mdb_migration.tester())


@mark.e2e_vm_migration_shardedcluster_no_auth
def test_deploy_vm_sharded(
    namespace: str,
    vm_sharded_mongod_sts,
    vm_sharded_mongos_sts,
    vm_sharded_service,
    vm_sharded_mongos_service,
):
    def mongod_sts_is_ready():
        sts = get_statefulset(namespace, vm_sharded_mongod_sts["metadata"]["name"])
        return sts.status.ready_replicas == vm_sharded_mongod_sts["spec"]["replicas"]

    def mongos_sts_is_ready():
        sts = get_statefulset(namespace, vm_sharded_mongos_sts["metadata"]["name"])
        return sts.status.ready_replicas == vm_sharded_mongos_sts["spec"]["replicas"]

    KubernetesTester.wait_until(mongod_sts_is_ready, timeout=300)
    KubernetesTester.wait_until(mongos_sts_is_ready, timeout=300)


@mark.e2e_vm_migration_shardedcluster_no_auth
def test_configure_ac(
    namespace: str,
    om_tester: OMTester,
    vm_sharded_mongod_sts,
    vm_sharded_mongos_sts,
    vm_sharded_service,
    vm_sharded_mongos_service,
    custom_mdb_version: str,
):
    mdb_version = ensure_ent_version(custom_mdb_version)
    ac = om_tester.api_get_automation_config()
    if len(ac.get("processes", [])) > 0:
        return

    ac = build_sharded_cluster_ac(
        om_tester,
        mongod_sts_name=MONGOD_STS_NAME,
        mongos_sts_name=MONGOS_STS_NAME,
        service_name=MONGOD_SVC_NAME,
        mongos_service_name=MONGOS_SVC_NAME,
        namespace=namespace,
        mongodb_version=mdb_version,
        config_rs_name=VM_CONFIG_RS_NAME,
        shard_rs_name=VM_SHARD_RS_NAME,
        config_server_count=CONFIG_SERVER_COUNT,
        shard_count=SHARD_COUNT,
        mongos_count=MONGOS_COUNT,
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
def test_common_generated_cr_shape(generated_cr: dict):
    assert_common_generated_sharded_cr_shape(
        generated_cr,
        expected_config_count=CONFIG_SERVER_COUNT,
        expected_shard_count=SHARD_COUNT,
        expected_mongos_count=MONGOS_COUNT,
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
    vm_total = CONFIG_SERVER_COUNT + SHARD_COUNT + MONGOS_COUNT
    assert len(ac_tester.get_all_processes()) == vm_total
    assert len(ac_tester.get_monitoring_versions()) == vm_total
    assert len(ac_tester.get_backup_versions()) == vm_total
    assert len(ac_tester.get_sharding_entries()) == 1


@mark.e2e_vm_migration_shardedcluster_no_auth
def test_migration_dry_run_connectivity_passes(mdb_migration: MongoDB):
    run_migration_dry_run_connectivity_passes(mdb_migration)


@mark.e2e_vm_migration_shardedcluster_no_auth
def test_migrate_vm_to_kubernetes(mdb_migration: MongoDB):
    mdb_migration.assert_reaches_phase(Phase.Running, timeout=1800)


@mark.e2e_vm_migration_shardedcluster_no_auth
@skip_if_local()
def test_connectivity_after_migration(mdb_migration: MongoDB):
    """Sharded cluster remains reachable without authentication after migration."""
    mdb_migration.tester().assert_connectivity()


@mark.e2e_vm_migration_shardedcluster_no_auth
def test_migration_data_exists_after_migration(mdb_migration: MongoDB):
    assert_migration_data_exists(mdb_migration.tester())


@mark.e2e_vm_migration_shardedcluster_no_auth
@skip_if_local()
def test_start_background_health_checker(mdb_health_checker: MongoDBBackgroundTester):
    mdb_health_checker.start()


@mark.e2e_vm_migration_shardedcluster_no_auth
def test_promote_and_prune_config_server(mdb_migration: MongoDB, om_tester: OMTester):
    try_load(mdb_migration)
    for i in range(CONFIG_SERVER_COUNT):
        mdb_migration["spec"]["memberConfig"][i]["priority"] = "1"
        mdb_migration["spec"]["memberConfig"][i]["votes"] = 1
        mdb_migration.update()
        mdb_migration.assert_reaches_phase(Phase.Running)

        config_external = [
            m for m in mdb_migration["spec"]["externalMembers"] if m["replicaSetName"] == VM_CONFIG_RS_NAME
        ]
        if config_external:
            mdb_migration["spec"]["externalMembers"].remove(config_external[-1])
            mdb_migration.update()
            mdb_migration.assert_reaches_phase(Phase.Running)

        om_tester.assert_cluster_available(VM_MONGOS_NAME)


@mark.e2e_vm_migration_shardedcluster_no_auth
def test_promote_and_prune_shard(mdb_migration: MongoDB, om_tester: OMTester):
    try_load(mdb_migration)
    shard_external = [m for m in mdb_migration["spec"]["externalMembers"] if m["replicaSetName"] == VM_SHARD_RS_NAME]
    for _ in range(len(shard_external)):
        current = [m for m in mdb_migration["spec"]["externalMembers"] if m["replicaSetName"] == VM_SHARD_RS_NAME]
        if not current:
            break
        mdb_migration["spec"]["externalMembers"].remove(current[-1])
        mdb_migration.update()
        mdb_migration.assert_reaches_phase(Phase.Running)
        om_tester.assert_cluster_available(VM_MONGOS_NAME)


@mark.e2e_vm_migration_shardedcluster_no_auth
def test_prune_mongos(mdb_migration: MongoDB):
    try_load(mdb_migration)
    mongos_external = [m for m in mdb_migration["spec"]["externalMembers"] if m["type"] == "mongos"]
    for m in mongos_external:
        mdb_migration["spec"]["externalMembers"].remove(m)
    mdb_migration.update()
    mdb_migration.assert_reaches_phase(Phase.Running)


@mark.e2e_vm_migration_shardedcluster_no_auth
def test_connection_string_after_full_migration(mdb_migration: MongoDB):
    assert_connection_string_after_full_sharded_migration(mdb_migration)


@mark.e2e_vm_migration_shardedcluster_no_auth
def test_process_names(om_tester: OMTester, mdb_migration: MongoDB):
    assert_k8s_sharded_process_names(om_tester, mdb_migration)


@mark.e2e_vm_migration_shardedcluster_no_auth
@skip_if_local()
def test_mongodb_reachable_during_promote_and_prune(mdb_health_checker: MongoDBBackgroundTester):
    mdb_health_checker.assert_healthiness()
    mdb_health_checker.stop()


@mark.e2e_vm_migration_shardedcluster_no_auth
@skip_if_local()
def test_connectivity_after_promote(mdb_migration: MongoDB):
    """Sharded cluster remains reachable without authentication after promote and prune."""
    mdb_migration.tester().assert_connectivity()


@mark.e2e_vm_migration_shardedcluster_no_auth
def test_migration_data_exists_after_promote(mdb_migration: MongoDB):
    assert_migration_data_exists(mdb_migration.tester())
