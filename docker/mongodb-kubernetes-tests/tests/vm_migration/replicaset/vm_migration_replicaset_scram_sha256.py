"""
VM migration from a generated MongoDB resource with SCRAM authentication.

This test configures VM members in Ops Manager, runs kubectl-mongodb migrate-to-mck,
applies the generated resources, and verifies dry-run validation, data continuity,
connection strings, process names, and the promote and prune flow.
"""

from kubetester import create_or_update_secret, get_statefulset
from kubetester.kubetester import KubernetesTester, ensure_ent_version, fcv_from_version
from kubetester.mongodb import MongoDB
from kubetester.mongotester import MongoDBBackgroundTester, with_scram
from kubetester.omtester import OMContext, OMTester
from kubetester.operator import Operator
from kubetester.phase import Phase
from pytest import fixture, mark
from tests.vm_migration.vm_migration_dry_run import run_migration_dry_run_connectivity_passes
from tests.vm_migration.vm_migration_helpers import (
    apply_generated_mongodb_resource,
    apply_user_crs_and_verify_ac,
    assert_common_generated_cr_shape,
    assert_connection_string_after_full_migration,
    assert_connection_string_contains_current_hosts,
    assert_k8s_process_names,
    assert_max_voting_members_validation,
    assert_migration_data_exists,
    deploy_vm_service,
    deploy_vm_statefulset,
    generated_mongodb_doc,
    generated_user_docs,
    insert_migration_data,
    promote_and_prune,
    rotate_password_and_verify,
    run_generate_cr,
    vm_replica_set_tester,
)

RS_NAME = "vm-mongodb-rs"
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
def vm_sts(namespace: str, om_tester: OMTester):
    return deploy_vm_statefulset(namespace, om_tester)


@fixture(scope="module")
def vm_service(namespace: str):
    return deploy_vm_service(namespace)


def _configure_ac(namespace: str, om_tester: OMTester, vm_sts: dict, vm_service: dict, mdb_version: str):
    """Set up a SCRAM-authenticated replica set with users, roles, and rich mongod config."""
    mdb_version = ensure_ent_version(mdb_version)
    ac = om_tester.api_get_automation_config()
    if len(ac["processes"]) > 0:
        return

    sts_name = vm_sts["metadata"]["name"]
    svc_name = vm_service["metadata"]["name"]
    rs_name = f"{sts_name}-rs"

    ac["auth"] = {
        "usersWanted": [
            {
                "user": "mms-automation-agent",
                "db": "admin",
                "roles": [{"role": "root", "db": "admin"}],
                "mechanisms": ["SCRAM-SHA-256"],
                "scramSha256Creds": {
                    "iterationCount": 15000,
                    "salt": "VvGtJFS/4euDEKqliOPW6idGBu4SMey5HgtRoQ==",
                    "serverKey": "xsHGbx5OJnYtZS19a4EboChhlD3mhDt7qOJss+FrShY=",
                    "storedKey": "1z/5Z7A5mlHt5lu/ZXUig5bwrBfOn3tzqTzn93Bf/Oo=",
                },
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
                "scramSha256Creds": {
                    "iterationCount": 15000,
                    "salt": "wksiNA03uUywS7DhdN062N8rpp2wgN535t9V+A==",
                    "serverKey": "QWoYhFkf0f5fo3zM11wFVXw6eEDtWToNg3aCurCmIww=",
                    "storedKey": "kQXatG95rq6ZysZFr00M8hK0kN13VuxX1pV3xxUpYSE=",
                },
                "authenticationRestrictions": [],
            },
            {
                "user": "reporting-user",
                "db": "admin",
                "roles": [{"role": "read", "db": "reporting"}],
                "mechanisms": ["SCRAM-SHA-256"],
                "scramSha256Creds": {
                    "iterationCount": 15000,
                    "salt": "Usm13I846IhrVWvzO1BCXn17qe2tWMHP+GXtKg==",
                    "serverKey": "0mGWp+V4qze1mWdoQLhss0OvL5smZ1VfineTeRYw4qE=",
                    "storedKey": "fLNS6LPqK12byCGG6wFexh5eNpniyAWouhKhaqODt7g=",
                },
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

    tags_per_member = [
        {"region": "us-east-1", "role": "primary"},
        {"region": "us-east-1", "role": "secondary"},
        {"region": "us-west-2", "role": "secondary"},
    ]

    # Member 2 intentionally diverges on logRotate, systemLog.logAppend, and oplogSizeMB
    # to exercise the generator's first-process-wins behaviour for additionalMongodConfig.
    log_rotate_per_member = [
        {
            "sizeThresholdMB": 1000,
            "timeThresholdHrs": 24,
            "numUncompressed": 5,
            "numTotal": 10,
            "percentOfDiskspace": 0.4,
        },
        {
            "sizeThresholdMB": 1000,
            "timeThresholdHrs": 24,
            "numUncompressed": 5,
            "numTotal": 10,
            "percentOfDiskspace": 0.4,
        },
        {
            "sizeThresholdMB": 2000,
            "timeThresholdHrs": 24,
            "numUncompressed": 3,
            "numTotal": 10,
            "percentOfDiskspace": 0.4,
        },
    ]
    log_append_per_member = [True, True, False]
    oplog_size_per_member = [2048, 2048, None]

    ac["processes"] = []
    ac["monitoringVersions"] = []
    ac["backupVersions"] = []
    ac["replicaSets"] = [{"_id": rs_name, "members": [], "protocolVersion": "1"}]

    for i in range(vm_sts["spec"]["replicas"]):
        hostname = f"{sts_name}-{i}.{svc_name}.{namespace}.svc.cluster.local"
        member_config_index = i if i < len(log_rotate_per_member) else 0

        ac["monitoringVersions"].append(
            {
                "hostname": hostname,
                "logPath": "/var/log/mongodb-mms-automation/monitoring-agent.log",
                "logRotate": {"sizeThresholdMB": 1000, "timeThresholdHrs": 24},
            }
        )

        ac["backupVersions"].append(
            {
                "hostname": hostname,
                "logPath": "/var/log/mongodb-mms-automation/backup-agent.log",
                "logRotate": {"sizeThresholdMB": 1000, "timeThresholdHrs": 24},
            }
        )

        replication = {"replSetName": rs_name}
        oplog_size = oplog_size_per_member[member_config_index]
        if oplog_size is not None:
            replication["oplogSizeMB"] = oplog_size

        ac["processes"].append(
            {
                "version": mdb_version,
                "name": f"{sts_name}-{i}",
                "hostname": hostname,
                "logRotate": log_rotate_per_member[member_config_index],
                "auditLogRotate": {
                    "sizeThresholdMB": 500,
                    "timeThresholdHrs": 48,
                    "numUncompressed": 2,
                    "numTotal": 10,
                    "percentOfDiskspace": 0.4,
                },
                "authSchemaVersion": 5,
                "featureCompatibilityVersion": fcv_from_version(mdb_version),
                "processType": "mongod",
                "args2_6": {
                    "net": {
                        "port": 27017,
                        "tls": {"mode": "disabled"},
                        "compression": {"compressors": "zlib,zstd"},
                    },
                    "storage": {
                        "dbPath": "/data/",
                        "directoryPerDB": True,
                    },
                    "systemLog": {
                        "path": "/data/mongodb.log",
                        "destination": "file",
                        "logAppend": log_append_per_member[member_config_index],
                    },
                    "replication": replication,
                    "auditLog": {
                        "destination": "file",
                        "format": "JSON",
                        "path": "/var/log/mongodb-mms-automation/mongodb-audit-changed.log",
                    },
                },
            }
        )

        member_tags = tags_per_member[i] if i < len(tags_per_member) else {}
        ac["replicaSets"][0]["members"].append(
            {
                "_id": i + 100,
                "host": f"{sts_name}-{i}",
                "priority": 1,
                "votes": 1,
                "secondaryDelaySecs": 0,
                "hidden": False,
                "arbiterOnly": False,
                "tags": member_tags,
            }
        )

    om_tester.api_put_automation_config(ac)


@fixture(scope="module")
def generated_cr_yaml(namespace: str) -> str:
    create_or_update_secret(namespace, "app-user-secret", {"password": APP_USER_PASSWORD})
    create_or_update_secret(namespace, "reporting-user-secret", {"password": REPORTING_USER_PASSWORD})
    return run_generate_cr(
        namespace,
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
    return apply_generated_mongodb_resource(namespace, generated_cr_yaml, customer_sets_disabled_tls_mode=True)


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


@mark.e2e_vm_migration_replicaset_scram_sha256
def test_deploy_vm(namespace: str, vm_sts, vm_service):
    def sts_is_ready():
        sts = get_statefulset(namespace, vm_sts["metadata"]["name"])
        return sts.status.ready_replicas == vm_sts["spec"]["replicas"]

    KubernetesTester.wait_until(sts_is_ready, timeout=300)


@mark.e2e_vm_migration_replicaset_scram_sha256
def test_configure_ac(namespace: str, om_tester: OMTester, vm_sts, vm_service, custom_mdb_version):
    _configure_ac(namespace, om_tester, vm_sts, vm_service, custom_mdb_version)
    om_tester.wait_agents_ready(timeout=600)


@mark.e2e_vm_migration_replicaset_scram_sha256
def test_user_connectivity_before_migration(namespace: str, scram_opts: list[dict]):
    """Users can authenticate against the VM replica set before migration."""
    vm_replica_set_tester(namespace).assert_scram_sha_authentication(
        username="app-user", password=APP_USER_PASSWORD, auth_mechanism="SCRAM-SHA-256"
    )


@mark.e2e_vm_migration_replicaset_scram_sha256
def test_insert_migration_data(namespace: str, scram_opts: list[dict]):
    insert_migration_data(vm_replica_set_tester(namespace), opts=scram_opts)


@mark.e2e_vm_migration_replicaset_scram_sha256
def test_install_operator(operator: Operator):
    operator.assert_is_running()


# Generated CR checks


@mark.e2e_vm_migration_replicaset_scram_sha256
def test_common_generated_cr_shape(generated_cr_yaml: str, generated_cr: dict, vm_sts: dict, version_id: str):
    assert_common_generated_cr_shape(generated_cr_yaml, generated_cr, version_id, vm_sts["spec"]["replicas"])


@mark.e2e_vm_migration_replicaset_scram_sha256
def test_user_crs_emitted(generated_cr_yaml: str):
    user_docs = generated_user_docs(generated_cr_yaml)
    usernames = {d["spec"]["username"] for d in user_docs}
    assert usernames == {"app-user", "reporting-user"}, f"Unexpected user CRs: {usernames}"


@mark.e2e_vm_migration_replicaset_scram_sha256
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


@mark.e2e_vm_migration_replicaset_scram_sha256
def test_settings_sourced_from_source_process(generated_cr_yaml: str):
    """When per-member config diverges, settings are taken from the source process (member 0).
    Member 2 has logAppend=False and no oplogSizeMB -- neither should affect the generated CR."""
    cr = generated_mongodb_doc(generated_cr_yaml)
    sl = cr["spec"].get("agent", {}).get("mongod", {}).get("systemLog", {})
    assert sl.get("destination") == "file", f"Expected destination=file, got: {sl}"
    assert sl.get("path") == "/data/mongodb.log", f"Expected path=/data/mongodb.log, got: {sl}"
    repl = cr["spec"].get("additionalMongodConfig", {}).get("replication", {})
    assert repl.get("oplogSizeMB") == 2048, f"Expected oplogSizeMB=2048 from source process, got: {repl}"


@mark.e2e_vm_migration_replicaset_scram_sha256
def test_vm_deployment_automation_config(om_tester: OMTester, vm_sts):
    ac_tester = om_tester.get_automation_config_tester()

    assert len(ac_tester.get_all_processes()) == vm_sts["spec"]["replicas"]
    assert len(ac_tester.get_monitoring_versions()) == vm_sts["spec"]["replicas"]
    assert len(ac_tester.get_backup_versions()) == vm_sts["spec"]["replicas"]
    assert len(ac_tester.get_replica_set_processes(RS_NAME)) == vm_sts["spec"]["replicas"]


# Lifecycle checks


@mark.e2e_vm_migration_replicaset_scram_sha256
def test_migration_dry_run_connectivity_passes(mdb_migration: MongoDB):
    """Operator validates connectivity to all externalMembers, then the annotation is removed."""
    run_migration_dry_run_connectivity_passes(mdb_migration)


@mark.e2e_vm_migration_replicaset_scram_sha256
def test_migrate_vm_to_kubernetes(mdb_migration: MongoDB):
    mdb_migration.assert_reaches_phase(Phase.Running, timeout=1200)
    assert_connection_string_contains_current_hosts(mdb_migration)


@mark.e2e_vm_migration_replicaset_scram_sha256
def test_max_voting_members_validation(mdb_migration: MongoDB):
    assert_max_voting_members_validation(mdb_migration)


@mark.e2e_vm_migration_replicaset_scram_sha256
def test_custom_roles_preserved_in_automation_config(om_tester: OMTester):
    """The custom roles remain in the operator-managed automation config after migration."""
    ac = om_tester.api_get_automation_config()
    names = {f"{r['role']}@{r['db']}" for r in ac.get("roles", [])}
    assert names >= {"appReadOnly@myapp", "metricsReader@admin"}, f"Custom roles missing after migration: {names}"


@mark.e2e_vm_migration_replicaset_scram_sha256
def test_user_crs_reach_updated(generated_cr_yaml: str, namespace: str, mdb_migration: MongoDB, om_tester: OMTester):
    apply_user_crs_and_verify_ac(generated_cr_yaml, namespace, om_tester)


@mark.e2e_vm_migration_replicaset_scram_sha256
def test_user_connectivity_after_migration(mdb_migration: MongoDB):
    """Users can still authenticate after the operator takes over the replica set."""
    mdb_migration.tester(use_ssl=False).assert_scram_sha_authentication(
        username="app-user", password=APP_USER_PASSWORD, auth_mechanism="SCRAM-SHA-256"
    )


@mark.e2e_vm_migration_replicaset_scram_sha256
def test_migration_data_exists_after_migration(mdb_migration: MongoDB, scram_opts: list[dict]):
    assert_migration_data_exists(mdb_migration.tester(use_ssl=False), opts=scram_opts)


@mark.e2e_vm_migration_replicaset_scram_sha256
def test_start_background_health_checker(mdb_health_checker: MongoDBBackgroundTester):
    mdb_health_checker.start()


@mark.e2e_vm_migration_replicaset_scram_sha256
def test_promote_and_prune(mdb_migration: MongoDB, vm_sts):
    promote_and_prune(mdb_migration, vm_sts)


@mark.e2e_vm_migration_replicaset_scram_sha256
def test_mongodb_reachable_during_promote_and_prune(mdb_health_checker: MongoDBBackgroundTester):
    mdb_health_checker.assert_healthiness()
    mdb_health_checker.stop()


@mark.e2e_vm_migration_replicaset_scram_sha256
def test_connection_string_after_full_migration(mdb_migration: MongoDB):
    assert_connection_string_after_full_migration(mdb_migration)


@mark.e2e_vm_migration_replicaset_scram_sha256
def test_process_names(om_tester: OMTester, mdb_migration: MongoDB):
    assert_k8s_process_names(om_tester, mdb_migration)


@mark.e2e_vm_migration_replicaset_scram_sha256
def test_user_connectivity_after_promote(mdb_migration: MongoDB):
    """Users can still authenticate after promote and prune completes."""
    mdb_migration.tester(use_ssl=False).assert_scram_sha_authentication(
        username="app-user", password=APP_USER_PASSWORD, auth_mechanism="SCRAM-SHA-256"
    )


@mark.e2e_vm_migration_replicaset_scram_sha256
def test_migration_data_exists_after_promote(mdb_migration: MongoDB, scram_opts: list[dict]):
    assert_migration_data_exists(mdb_migration.tester(use_ssl=False), opts=scram_opts)


@mark.e2e_vm_migration_replicaset_scram_sha256
def test_password_rotation_keeps_migrated_flag(generated_cr_yaml: str, namespace: str, om_tester: OMTester):
    rotate_password_and_verify(generated_cr_yaml, namespace, om_tester, target_username="app-user")
