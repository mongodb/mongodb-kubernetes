"""
VM migration from a generated MongoDB sharded cluster resource with SCRAM-SHA-1 authentication.

This mirrors the SCRAM-SHA-256 sharded cluster migration test but exercises the legacy SCRAM-SHA-1
mechanism: the VM automation config seeds SCRAM-SHA-1 credentials (computed from the password),
and the generated CR must carry auth mode SCRAM-SHA-1 and a MongoDBUser CR for the application user.
"""

from kubetester import create_or_update_secret, get_statefulset, try_load
from kubetester.kubetester import KubernetesTester, ensure_ent_version
from kubetester.mongodb import MongoDB
from kubetester.mongotester import MongoDBBackgroundTester, with_scram
from kubetester.omtester import OMContext, OMTester
from kubetester.operator import Operator
from kubetester.phase import Phase
from kubetester.scram import build_sha1_creds
from pytest import fixture, mark
from tests.vm_migration.vm_migration_dry_run import run_migration_dry_run_connectivity_passes
from tests.vm_migration.vm_migration_helpers import (
    apply_generated_sharded_cluster_resource,
    apply_user_crs_and_verify_ac,
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
AGENT_USER = "mms-automation-agent"
AGENT_PASSWORD = "agent-sha1-password"
APP_USER = "app-user"
APP_USER_PASSWORD = "appUserSha1!"
SCRAM_MECHANISM = "SCRAM-SHA-1"
# SCRAM-SHA-1 requires MONGODB-CR in agent mode for legacy support.
AGENT_MECHANISM = "MONGODB-CR"


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


def _configure_ac_scram_sha1(namespace: str, om_tester: OMTester, mdb_version: str):
    """SCRAM-SHA-1 sharded cluster. SCRAM-SHA-1 is deprecated and requires MONGODB-CR in agent mode for
    legacy support, otherwise Ops Manager rejects it with "Cannot configure SCRAM-SHA-1 without using
    MONGODB-CR in the Agent Mode". The agent authenticates with MONGODB-CR while the deployment accepts
    both SCRAM-SHA-1 and MONGODB-CR. Credentials are computed from the passwords so the agent and app
    user can authenticate before migration."""
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
    )

    ac["auth"] = {
        "usersWanted": [
            {
                "user": AGENT_USER,
                "db": "admin",
                "roles": [{"role": "root", "db": "admin"}],
                "mechanisms": [SCRAM_MECHANISM],
                "scramSha1Creds": build_sha1_creds(AGENT_USER, AGENT_PASSWORD),
                "authenticationRestrictions": [],
            },
            {
                "user": APP_USER,
                "db": "admin",
                "roles": [
                    {"role": "readWrite", "db": "admin"},
                    {"role": "readWrite", "db": "migration_data"},
                ],
                "mechanisms": [SCRAM_MECHANISM],
                "scramSha1Creds": build_sha1_creds(APP_USER, APP_USER_PASSWORD),
                "authenticationRestrictions": [],
            },
        ],
        "usersDeleted": [],
        "disabled": False,
        "authoritativeSet": False,
        "deploymentAuthMechanisms": [SCRAM_MECHANISM, AGENT_MECHANISM],
        "autoAuthMechanisms": [AGENT_MECHANISM],
        "autoAuthMechanism": AGENT_MECHANISM,
        "autoUser": AGENT_USER,
        "autoAuthRestrictions": [],
        "autoPwd": AGENT_PASSWORD,
        "key": "bXlrZXlmaWxlY29udGVudHM=",
        "keyfile": "/var/lib/mongodb-mms-automation/keyfile",
        "keyfileWindows": "%SystemDrive%\\MMSAutomation\\versions\\keyfile",
    }

    om_tester.api_put_automation_config(ac)


@fixture(scope="module")
def generated_cr_yaml(namespace: str) -> str:
    create_or_update_secret(namespace, "app-user-secret", {"password": APP_USER_PASSWORD})
    return run_generate_cr(
        namespace,
        resource_name_override=MDB_RESOURCE_NAME,
        user_secrets={f"{APP_USER}:admin": "app-user-secret"},
    )


@fixture(scope="module")
def generated_cr(generated_cr_yaml: str) -> dict:
    return generated_mongodb_doc(generated_cr_yaml)


@fixture(scope="module")
def mdb_migration(namespace: str, generated_cr_yaml: str) -> MongoDB:
    return apply_generated_sharded_cluster_resource(
        namespace,
        generated_cr_yaml,
        config_rs_name=VM_CONFIG_RS_NAME,
        customer_sets_disabled_tls_mode=True,
    )


@fixture(scope="module")
def scram_opts() -> list[dict]:
    return [with_scram(APP_USER, APP_USER_PASSWORD, SCRAM_MECHANISM)]


@fixture(scope="module")
def mdb_health_checker(mdb_migration: MongoDB, scram_opts: list[dict]) -> MongoDBBackgroundTester:
    return MongoDBBackgroundTester(
        mdb_migration.tester(use_ssl=False),
        health_function_params={"attempts": 1, "opts": scram_opts},
    )


# Test flow


@mark.e2e_vm_migration_shardedcluster_scram_sha1
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


@mark.e2e_vm_migration_shardedcluster_scram_sha1
def test_configure_ac(namespace: str, om_tester: OMTester, custom_mdb_version: str):
    ac = om_tester.api_get_automation_config()
    if len(ac.get("processes", [])) > 0:
        return
    _configure_ac_scram_sha1(namespace, om_tester, ensure_ent_version(custom_mdb_version))
    om_tester.wait_agents_ready(timeout=600)


@mark.e2e_vm_migration_shardedcluster_scram_sha1
def test_user_connectivity_before_migration(namespace: str):
    """Users can authenticate against the VM sharded cluster before migration."""
    vm_mongos_tester(MONGOS_STS_NAME, MONGOS_SVC_NAME, namespace).assert_scram_sha_authentication(
        username=APP_USER, password=APP_USER_PASSWORD, auth_mechanism=SCRAM_MECHANISM
    )


@mark.e2e_vm_migration_shardedcluster_scram_sha1
def test_insert_migration_data(namespace: str, scram_opts: list[dict]):
    insert_migration_data(vm_mongos_tester(MONGOS_STS_NAME, MONGOS_SVC_NAME, namespace), opts=scram_opts)


@mark.e2e_vm_migration_shardedcluster_scram_sha1
def test_install_operator(operator: Operator):
    operator.assert_is_running()


# Generated CR checks


@mark.e2e_vm_migration_shardedcluster_scram_sha1
def test_common_generated_cr_shape(generated_cr: dict):
    assert_common_generated_sharded_cr_shape(generated_cr, CONFIG_SERVER_COUNT, SHARD_COUNT, MONGOS_COUNT)


@mark.e2e_vm_migration_shardedcluster_scram_sha1
def test_scram_sha1_mode_in_cr(generated_cr: dict):
    auth = generated_cr["spec"]["security"]["authentication"]
    assert SCRAM_MECHANISM in auth["modes"]
    assert AGENT_MECHANISM in auth["modes"]
    assert auth["agents"]["mode"] == AGENT_MECHANISM


@mark.e2e_vm_migration_shardedcluster_scram_sha1
def test_user_cr_emitted(generated_cr_yaml: str):
    user_docs = generated_user_docs(generated_cr_yaml)
    usernames = {d["spec"]["username"] for d in user_docs}
    assert usernames == {APP_USER}, f"Unexpected user CRs: {usernames}"


@mark.e2e_vm_migration_shardedcluster_scram_sha1
def test_vm_deployment_automation_config(om_tester: OMTester):
    ac_tester = om_tester.get_automation_config_tester()
    vm_total = CONFIG_SERVER_COUNT + SHARD_COUNT + MONGOS_COUNT
    assert len(ac_tester.get_all_processes()) == vm_total
    assert len(ac_tester.get_monitoring_versions()) == vm_total
    assert len(ac_tester.get_backup_versions()) == vm_total
    assert len(ac_tester.get_sharding_entries()) == 1


# Lifecycle checks


@mark.e2e_vm_migration_shardedcluster_scram_sha1
def test_migration_dry_run_connectivity_passes(mdb_migration: MongoDB):
    run_migration_dry_run_connectivity_passes(mdb_migration)


@mark.e2e_vm_migration_shardedcluster_scram_sha1
def test_migrate_vm_to_kubernetes(mdb_migration: MongoDB):
    mdb_migration.assert_reaches_phase(Phase.Running, timeout=1800)


@mark.e2e_vm_migration_shardedcluster_scram_sha1
def test_user_crs_reach_updated(generated_cr_yaml: str, namespace: str, mdb_migration: MongoDB, om_tester: OMTester):
    apply_user_crs_and_verify_ac(generated_cr_yaml, namespace, om_tester)


@mark.e2e_vm_migration_shardedcluster_scram_sha1
def test_user_connectivity_after_migration(mdb_migration: MongoDB):
    """Users can still authenticate after the operator takes over the sharded cluster."""
    mdb_migration.tester(use_ssl=False).assert_scram_sha_authentication(
        username=APP_USER, password=APP_USER_PASSWORD, auth_mechanism=SCRAM_MECHANISM
    )


@mark.e2e_vm_migration_shardedcluster_scram_sha1
def test_migration_data_exists_after_migration(mdb_migration: MongoDB, scram_opts: list[dict]):
    assert_migration_data_exists(mdb_migration.tester(use_ssl=False), opts=scram_opts)


@mark.e2e_vm_migration_shardedcluster_scram_sha1
def test_start_background_health_checker(mdb_health_checker: MongoDBBackgroundTester):
    mdb_health_checker.start()


@mark.e2e_vm_migration_shardedcluster_scram_sha1
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


@mark.e2e_vm_migration_shardedcluster_scram_sha1
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


@mark.e2e_vm_migration_shardedcluster_scram_sha1
def test_prune_mongos(mdb_migration: MongoDB):
    try_load(mdb_migration)
    mongos_external = [m for m in mdb_migration["spec"]["externalMembers"] if m["type"] == "mongos"]
    for m in mongos_external:
        mdb_migration["spec"]["externalMembers"].remove(m)
    mdb_migration.update()
    mdb_migration.assert_reaches_phase(Phase.Running)


@mark.e2e_vm_migration_shardedcluster_scram_sha1
def test_mongodb_reachable_during_promote_and_prune(mdb_health_checker: MongoDBBackgroundTester):
    mdb_health_checker.assert_healthiness()
    mdb_health_checker.stop()


@mark.e2e_vm_migration_shardedcluster_scram_sha1
def test_connection_string_after_full_migration(mdb_migration: MongoDB):
    assert_connection_string_after_full_sharded_migration(mdb_migration)


@mark.e2e_vm_migration_shardedcluster_scram_sha1
def test_process_names(om_tester: OMTester, mdb_migration: MongoDB):
    assert_k8s_sharded_process_names(om_tester, mdb_migration)


@mark.e2e_vm_migration_shardedcluster_scram_sha1
def test_user_connectivity_after_promote(mdb_migration: MongoDB):
    """Users can still authenticate after promote and prune completes."""
    mdb_migration.tester(use_ssl=False).assert_scram_sha_authentication(
        username=APP_USER, password=APP_USER_PASSWORD, auth_mechanism=SCRAM_MECHANISM
    )


@mark.e2e_vm_migration_shardedcluster_scram_sha1
def test_migration_data_exists_after_promote(mdb_migration: MongoDB, scram_opts: list[dict]):
    assert_migration_data_exists(mdb_migration.tester(use_ssl=False), opts=scram_opts)
