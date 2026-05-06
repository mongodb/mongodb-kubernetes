"""
VM migration test using kubectl-mongodb migrate.

Configures a realistic automation config with:
  - SCRAM-SHA-256 auth with automation agent user + two app users
  - Custom role (appReadOnly)
  - logRotate + auditLogRotate per process
  - args2_6: compression, oplogSizeMB, directoryPerDB, setParameter,
    auditLog, systemLog.logAppend
  - Member tags (region / role)
  - FCV

Pre-creates Kubernetes Secrets for each SCRAM user, then passes them to
the migrate tool via --users-secrets-file. Runs the full promote-and-prune lifecycle.
"""

import yaml
from kubetester import create_or_update_secret, get_statefulset, try_load
from kubetester.kubetester import KubernetesTester, ensure_ent_version, fcv_from_version
from kubetester.mongodb import MongoDB
from kubetester.omtester import OMContext, OMTester
from kubetester.operator import Operator
from kubetester.phase import Phase
from pytest import fixture, mark
from tests.tls.vm_migration_dry_run import run_migration_dry_run_connectivity_passes
from tests.tls.vm_migration_helpers import (
    apply_user_crs_and_verify_ac,
    assert_migration_dry_run_annotation,
    deploy_vm_service,
    deploy_vm_statefulset,
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
        }
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
    ac["replicaSets"] = [{"_id": rs_name, "members": [], "protocolVersion": "1"}]

    for i in range(vm_sts["spec"]["replicas"]):
        hostname = f"{sts_name}-{i}.{svc_name}.{namespace}.svc.cluster.local"

        ac["monitoringVersions"].append(
            {
                "hostname": hostname,
                "logPath": "/var/log/mongodb-mms-automation/monitoring-agent.log",
                "logRotate": {"sizeThresholdMB": 1000, "timeThresholdHrs": 24},
            }
        )

        replication = {"replSetName": rs_name}
        if oplog_size_per_member[i] is not None:
            replication["oplogSizeMB"] = oplog_size_per_member[i]

        ac["processes"].append(
            {
                "version": mdb_version,
                "name": f"{sts_name}-{i}",
                "hostname": hostname,
                "logRotate": log_rotate_per_member[i],
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
                        "logAppend": log_append_per_member[i],
                    },
                    "replication": replication,
                    "setParameter": {
                        "authenticationMechanisms": "SCRAM-SHA-256",
                    },
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
def mdb_migration(namespace: str, generated_cr_yaml: str) -> MongoDB:
    resource = MongoDB(RS_NAME, namespace)
    if try_load(resource):
        return resource

    resource.backing_obj = next(yaml.safe_load_all(generated_cr_yaml))
    resource.backing_obj.setdefault("spec", {}).setdefault("additionalMongodConfig", {}).setdefault(
        "net", {}
    ).setdefault("tls", {})["mode"] = "disabled"
    # The generated CR starts with members=0 and no memberConfig.
    # Set members to match the VM replica count and add draining memberConfig.
    num_members = len(resource.backing_obj["spec"].get("externalMembers", []))
    resource.backing_obj["spec"]["members"] = num_members
    resource.backing_obj["spec"]["memberConfig"] = [{"votes": 0, "priority": "0"} for _ in range(num_members)]
    resource.update()
    return resource


# ---------------------------------------------------------------------------
# Tests
# ---------------------------------------------------------------------------


@mark.e2e_vm_migration_generate_scram_full
def test_deploy_vm(namespace: str, vm_sts, vm_service):
    def sts_is_ready():
        sts = get_statefulset(namespace, vm_sts["metadata"]["name"])
        return sts.status.ready_replicas == 3

    KubernetesTester.wait_until(sts_is_ready, timeout=300)


@mark.e2e_vm_migration_generate_scram_full
def test_configure_ac(namespace: str, om_tester: OMTester, vm_sts, vm_service, custom_mdb_version):
    _configure_ac(namespace, om_tester, vm_sts, vm_service, custom_mdb_version)


@mark.e2e_vm_migration_generate_scram_full
def test_user_connectivity_before_migration(namespace: str):
    """Users can authenticate against the VM replica set before migration."""
    vm_replica_set_tester(namespace).assert_scram_sha_authentication(
        username="app-user", password=APP_USER_PASSWORD, auth_mechanism="SCRAM-SHA-256"
    )


@mark.e2e_vm_migration_generate_scram_full
def test_install_operator(operator: Operator):
    operator.assert_is_running()


# --- Generated CR checks (all run immediately after generation, before any lifecycle test) ---


@mark.e2e_vm_migration_generate_scram_full
def test_migration_dry_run_annotation_present(generated_cr_yaml: str):
    """Generated MongoDB CR must carry the migration-dry-run annotation."""
    assert_migration_dry_run_annotation(generated_cr_yaml)


@mark.e2e_vm_migration_generate_scram_full
def test_user_crs_emitted(generated_cr_yaml: str):
    docs = list(yaml.safe_load_all(generated_cr_yaml))
    user_docs = [d for d in docs if d and d.get("kind") == "MongoDBUser"]
    usernames = {d["spec"]["username"] for d in user_docs}
    assert usernames == {"app-user", "reporting-user"}, f"Unexpected user CRs: {usernames}"


@mark.e2e_vm_migration_generate_scram_full
def test_settings_sourced_from_source_process(generated_cr_yaml: str):
    """When per-member config diverges, settings are taken from the source process (member 0).
    Member 2 has logAppend=False and no oplogSizeMB -- neither should affect the generated CR."""
    cr = next(yaml.safe_load_all(generated_cr_yaml))
    sl = cr["spec"].get("agent", {}).get("mongod", {}).get("systemLog", {})
    assert sl.get("destination") == "file", f"Expected destination=file, got: {sl}"
    assert sl.get("path") == "/data/mongodb.log", f"Expected path=/data/mongodb.log, got: {sl}"
    repl = cr["spec"].get("additionalMongodConfig", {}).get("replication", {})
    assert repl.get("oplogSizeMB") == 2048, f"Expected oplogSizeMB=2048 from source process, got: {repl}"


# --- Lifecycle tests ---


@mark.e2e_vm_migration_generate_scram_full
def test_migration_dry_run_connectivity_passes(mdb_migration: MongoDB):
    """Operator validates connectivity to all externalMembers, then the annotation is removed."""
    run_migration_dry_run_connectivity_passes(mdb_migration)


@mark.e2e_vm_migration_generate_scram_full
def test_migrate_vm_to_kubernetes(mdb_migration: MongoDB):
    mdb_migration.assert_reaches_phase(Phase.Running, timeout=1200)


@mark.e2e_vm_migration_generate_scram_full
def test_user_crs_reach_updated(generated_cr_yaml: str, namespace: str, mdb_migration: MongoDB, om_tester: OMTester):
    apply_user_crs_and_verify_ac(generated_cr_yaml, namespace, om_tester)


@mark.e2e_vm_migration_generate_scram_full
def test_user_connectivity_after_migration(mdb_migration: MongoDB):
    """Users can still authenticate after the operator takes over the replica set."""
    mdb_migration.tester(use_ssl=False).assert_scram_sha_authentication(
        username="app-user", password=APP_USER_PASSWORD, auth_mechanism="SCRAM-SHA-256"
    )


@mark.e2e_vm_migration_generate_scram_full
def test_promote_and_prune(mdb_migration: MongoDB, vm_sts):
    promote_and_prune(mdb_migration, vm_sts)


@mark.e2e_vm_migration_generate_scram_full
def test_user_connectivity_after_promote(mdb_migration: MongoDB):
    """Users can still authenticate after promote-and-prune completes."""
    mdb_migration.tester(use_ssl=False).assert_scram_sha_authentication(
        username="app-user", password=APP_USER_PASSWORD, auth_mechanism="SCRAM-SHA-256"
    )


@mark.e2e_vm_migration_generate_scram_full
def test_password_rotation_keeps_migrated_flag(generated_cr_yaml: str, namespace: str, om_tester: OMTester):
    rotate_password_and_verify(generated_cr_yaml, namespace, om_tester, target_username="app-user")
