"""
VM migration from a generated MongoDB sharded cluster resource with SCRAM-SHA-256 authentication.

This test configures VM members in Ops Manager, runs kubectl-mongodb migrate-to-mck,
applies the generated resources, and verifies dry-run validation, data continuity,
connection strings, process names, and the promote and prune flow for a sharded cluster.
"""

from kubetester import create_or_update_secret, get_statefulset, try_load
from kubetester.kubetester import KubernetesTester, ensure_ent_version
from kubetester.mongodb import MongoDB
from kubetester.mongotester import MongoDBBackgroundTester, with_scram
from kubetester.omtester import OMContext, OMTester
from kubetester.operator import Operator
from kubetester.phase import Phase
from kubetester.scram import build_sha256_creds
from pytest import fixture, mark
from tests.vm_migration.vm_migration_common_helper import (
    apply_user_crs_and_verify_ac,
    assert_max_voting_members_validation,
    assert_migration_data_exists,
    generated_mongodb_doc,
    generated_user_docs,
    insert_migration_data,
    rotate_password_and_verify,
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
    promote_and_prune_shard,
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
APP_USER_PASSWORD = "appUser123!"
REPORTING_USER_PASSWORD = "reporting456!"


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


def _configure_ac_scram_sha256(namespace: str, om_tester: OMTester, mdb_version: str):
    """Set up a SCRAM-SHA-256 authenticated sharded cluster with users and custom roles."""
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
    )

    ac["auth"] = {
        "usersWanted": [
            {
                "user": "mms-automation-agent",
                "db": "admin",
                "roles": [{"role": "root", "db": "admin"}],
                "mechanisms": ["SCRAM-SHA-256"],
                "scramSha256Creds": build_sha256_creds("mms-automation-agent-password"),
                "authenticationRestrictions": [],
            },
            {
                "user": "app-user",
                "db": "admin",
                "roles": [
                    {"role": "readWrite", "db": "admin"},
                    {"role": "readWrite", "db": "migration_data"},
                    {"role": "readWrite", "db": "myapp"},
                    {"role": "read", "db": "reporting"},
                ],
                "mechanisms": ["SCRAM-SHA-256"],
                "scramSha256Creds": build_sha256_creds(APP_USER_PASSWORD),
                "authenticationRestrictions": [],
            },
            {
                "user": "reporting-user",
                "db": "admin",
                "roles": [{"role": "read", "db": "reporting"}],
                "mechanisms": ["SCRAM-SHA-256"],
                "scramSha256Creds": build_sha256_creds(REPORTING_USER_PASSWORD),
                "authenticationRestrictions": [],
            },
        ],
        "usersDeleted": [],
        "disabled": False,
        "authoritativeSet": False,
        "deploymentAuthMechanisms": ["SCRAM-SHA-256"],
        "autoAuthMechanisms": ["SCRAM-SHA-256"],
        "autoAuthMechanism": "SCRAM-SHA-256",
        "autoUser": "mms-automation-agent",
        "autoAuthRestrictions": [],
        "autoPwd": "mms-automation-agent-password",
        "key": "bXlrZXlmaWxlY29udGVudHM=",
        "keyfile": "/var/lib/mongodb-mms-automation/keyfile",
        "keyfileWindows": "%SystemDrive%\\MMSAutomation\\versions\\keyfile",
    }

    ac["roles"] = [
        {
            "role": "appReadOnly",
            "db": "myapp",
            "privileges": [
                {
                    "resource": {"db": "myapp", "collection": ""},
                    "actions": ["find", "listCollections"],
                }
            ],
            "roles": [{"role": "read", "db": "myapp"}],
        },
        {
            "role": "metricsReader",
            "db": "admin",
            "privileges": [
                {
                    "resource": {"cluster": True},
                    "actions": ["serverStatus", "replSetGetStatus"],
                }
            ],
            "roles": [],
        },
    ]

    om_tester.api_put_automation_config(ac)


@fixture(scope="module")
def generated_cr_yaml(namespace: str) -> str:
    create_or_update_secret(namespace, "app-user-secret", {"password": APP_USER_PASSWORD})
    create_or_update_secret(namespace, "reporting-user-secret", {"password": REPORTING_USER_PASSWORD})
    return run_generate_cr(
        namespace,
        resource_name_override=MDB_RESOURCE_NAME,
        user_secrets={
            "app-user:admin": "app-user-secret",
            "reporting-user:admin": "reporting-user-secret",
        },
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
    return [with_scram("app-user", APP_USER_PASSWORD, "SCRAM-SHA-256")]


@fixture(scope="module")
def mdb_health_checker(mdb_migration: MongoDB, scram_opts: list[dict]) -> MongoDBBackgroundTester:
    return MongoDBBackgroundTester(
        mdb_migration.tester(use_ssl=False),
        health_function_params={"attempts": 1, "opts": scram_opts},
    )


# Test flow


@mark.e2e_vm_migration_shardedcluster_scram_sha256
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


@mark.e2e_vm_migration_shardedcluster_scram_sha256
def test_configure_ac(namespace: str, om_tester: OMTester, custom_mdb_version: str):
    ac = om_tester.api_get_automation_config()
    if len(ac.get("processes", [])) > 0:
        return
    _configure_ac_scram_sha256(namespace, om_tester, ensure_ent_version(custom_mdb_version))
    om_tester.wait_agents_ready(timeout=600)


@mark.e2e_vm_migration_shardedcluster_scram_sha256
def test_user_connectivity_before_migration(namespace: str, scram_opts: list[dict]):
    """Users can authenticate against the VM sharded cluster before migration."""
    vm_mongos_tester(MONGOS_STS_NAME, MONGOS_SVC_NAME, namespace).assert_scram_sha_authentication(
        username="app-user", password=APP_USER_PASSWORD, auth_mechanism="SCRAM-SHA-256"
    )


@mark.e2e_vm_migration_shardedcluster_scram_sha256
def test_insert_migration_data(namespace: str, scram_opts: list[dict]):
    insert_migration_data(vm_mongos_tester(MONGOS_STS_NAME, MONGOS_SVC_NAME, namespace), opts=scram_opts)


@mark.e2e_vm_migration_shardedcluster_scram_sha256
def test_install_operator(operator: Operator):
    operator.assert_is_running()


# Generated CR checks


@mark.e2e_vm_migration_shardedcluster_scram_sha256
def test_common_generated_cr_shape(generated_cr: dict):
    assert_common_generated_sharded_cr_shape(generated_cr, MIN_VM_CONFIGSRV, MIN_VM_SHARD, MIN_VM_MONGOS)


@mark.e2e_vm_migration_shardedcluster_scram_sha256
def test_user_crs_emitted(generated_cr_yaml: str):
    user_docs = generated_user_docs(generated_cr_yaml)
    usernames = {d["spec"]["username"] for d in user_docs}
    assert usernames == {"app-user", "reporting-user"}, f"Unexpected user CRs: {usernames}"


@mark.e2e_vm_migration_shardedcluster_scram_sha256
def test_custom_roles_in_generated_cr(generated_cr: dict):
    """The deployment's custom roles are carried into spec.security.roles."""
    roles = generated_cr["spec"]["security"].get("roles", [])
    names = {f"{r['role']}@{r['db']}" for r in roles}
    assert names == {"appReadOnly@myapp", "metricsReader@admin"}, f"Unexpected roles in generated CR: {roles}"
    app_role = next(r for r in roles if r["role"] == "appReadOnly")
    assert set(app_role["privileges"][0]["actions"]) == {
        "find",
        "listCollections",
    }, f"Unexpected privileges: {app_role}"


@mark.e2e_vm_migration_shardedcluster_scram_sha256
def test_vm_deployment_automation_config(om_tester: OMTester):
    ac_tester = om_tester.get_automation_config_tester()
    vm_total = MIN_VM_CONFIGSRV + MIN_VM_SHARD + MIN_VM_MONGOS
    assert len(ac_tester.get_all_processes()) == vm_total
    assert len(ac_tester.get_monitoring_versions()) == vm_total
    assert len(ac_tester.get_backup_versions()) == vm_total
    assert len(ac_tester.get_sharding_entries()) == 1


# Lifecycle checks


@mark.e2e_vm_migration_shardedcluster_scram_sha256
def test_migration_dry_run_connectivity_passes(mdb_migration: MongoDB):
    """Operator validates connectivity to all externalMembers, then the annotation is removed."""
    run_migration_dry_run_connectivity_passes(mdb_migration)


@mark.e2e_vm_migration_shardedcluster_scram_sha256
def test_migrate_vm_to_kubernetes(mdb_migration: MongoDB):
    mdb_migration.assert_reaches_phase(Phase.Running, timeout=1800)


@mark.e2e_vm_migration_shardedcluster_scram_sha256
def test_max_voting_members_validation(mdb_migration: MongoDB):
    assert_max_voting_members_validation(mdb_migration)


@mark.e2e_vm_migration_shardedcluster_scram_sha256
def test_custom_roles_preserved_in_automation_config(om_tester: OMTester):
    """The custom roles remain in the operator-managed automation config after migration."""
    ac = om_tester.api_get_automation_config()
    names = {f"{r['role']}@{r['db']}" for r in ac.get("roles", [])}
    assert names >= {"appReadOnly@myapp", "metricsReader@admin"}, f"Custom roles missing after migration: {names}"


@mark.e2e_vm_migration_shardedcluster_scram_sha256
def test_user_crs_reach_updated(generated_cr_yaml: str, namespace: str, mdb_migration: MongoDB, om_tester: OMTester):
    apply_user_crs_and_verify_ac(generated_cr_yaml, namespace, om_tester)


@mark.e2e_vm_migration_shardedcluster_scram_sha256
def test_user_connectivity_after_migration(mdb_migration: MongoDB):
    """Users can still authenticate after the operator takes over the sharded cluster."""
    mdb_migration.tester(use_ssl=False).assert_scram_sha_authentication(
        username="app-user", password=APP_USER_PASSWORD, auth_mechanism="SCRAM-SHA-256"
    )


@mark.e2e_vm_migration_shardedcluster_scram_sha256
def test_migration_data_exists_after_migration(mdb_migration: MongoDB, scram_opts: list[dict]):
    assert_migration_data_exists(mdb_migration.tester(use_ssl=False), opts=scram_opts)


@mark.e2e_vm_migration_shardedcluster_scram_sha256
def test_start_background_health_checker(mdb_health_checker: MongoDBBackgroundTester):
    mdb_health_checker.start()


@mark.e2e_vm_migration_shardedcluster_scram_sha256
def test_promote_and_prune_config_server(mdb_migration: MongoDB, om_tester: OMTester):
    try_load(mdb_migration)
    for i in range(MIN_VM_CONFIGSRV):
        mdb_migration["spec"]["memberConfig"][i]["priority"] = "1"
        mdb_migration["spec"]["memberConfig"][i]["votes"] = 1
        mdb_migration.update()
        mdb_migration.assert_reaches_phase(Phase.Running)

        config_external = [
            m for m in mdb_migration["spec"]["externalMembers"] if m.get("replicaSetName") == VM_CONFIG_RS_NAME
        ]
        if config_external:
            mdb_migration["spec"]["externalMembers"].remove(config_external[-1])
            mdb_migration.update()
            mdb_migration.assert_reaches_phase(Phase.Running)

        om_tester.assert_cluster_available(VM_MONGOS_NAME)


@mark.e2e_vm_migration_shardedcluster_scram_sha256
def test_prune_shard(mdb_migration: MongoDB, om_tester: OMTester):
    promote_and_prune_shard(mdb_migration, om_tester, VM_SHARD_RS_NAME, VM_MONGOS_NAME)


@mark.e2e_vm_migration_shardedcluster_scram_sha256
def test_prune_mongos(mdb_migration: MongoDB):
    try_load(mdb_migration)
    mongos_external = [m for m in mdb_migration["spec"]["externalMembers"] if m["type"] == "mongos"]
    for m in mongos_external:
        mdb_migration["spec"]["externalMembers"].remove(m)
    mdb_migration.update()
    mdb_migration.assert_reaches_phase(Phase.Running)


@mark.e2e_vm_migration_shardedcluster_scram_sha256
def test_mongodb_reachable_during_promote_and_prune(mdb_health_checker: MongoDBBackgroundTester):
    mdb_health_checker.assert_healthiness()
    mdb_health_checker.stop()


@mark.e2e_vm_migration_shardedcluster_scram_sha256
def test_connection_string_after_full_migration(mdb_migration: MongoDB):
    assert_connection_string_after_full_sharded_migration(mdb_migration)


@mark.e2e_vm_migration_shardedcluster_scram_sha256
def test_process_names(om_tester: OMTester, mdb_migration: MongoDB):
    assert_k8s_sharded_process_names(om_tester, mdb_migration)


@mark.e2e_vm_migration_shardedcluster_scram_sha256
def test_user_connectivity_after_promote(mdb_migration: MongoDB):
    """Users can still authenticate after promote and prune completes."""
    mdb_migration.tester(use_ssl=False).assert_scram_sha_authentication(
        username="app-user", password=APP_USER_PASSWORD, auth_mechanism="SCRAM-SHA-256"
    )


@mark.e2e_vm_migration_shardedcluster_scram_sha256
def test_migration_data_exists_after_promote(mdb_migration: MongoDB, scram_opts: list[dict]):
    assert_migration_data_exists(mdb_migration.tester(use_ssl=False), opts=scram_opts)


@mark.e2e_vm_migration_shardedcluster_scram_sha256
def test_password_rotation_keeps_migrated_flag(generated_cr_yaml: str, namespace: str, om_tester: OMTester):
    rotate_password_and_verify(generated_cr_yaml, namespace, om_tester, target_username="app-user")
