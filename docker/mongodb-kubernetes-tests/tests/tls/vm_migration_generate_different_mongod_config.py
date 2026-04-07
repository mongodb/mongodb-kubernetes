import yaml
from kubetester import get_statefulset, try_load
from kubetester.kubetester import KubernetesTester, ensure_ent_version, fcv_from_version
from kubetester.mongodb import MongoDB
from kubetester.omtester import OMContext, OMTester
from kubetester.operator import Operator
from kubetester.phase import Phase
from pytest import fixture, mark
from tests.tls.vm_migration_helpers import (
    apply_user_crs_and_verify_ac,
    deploy_vm_service,
    deploy_vm_statefulset,
    log_automation_config,
    log_automation_config_diff,
    rotate_password_and_verify,
    run_migrate_generate,
)

RS_NAME = "vm-mongodb-rs"
APP_USER_PASSWORD = "diffMongodCfgPwd123!"


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


# Per-member configurations that intentionally diverge.
_MEMBER_OVERRIDES = [
    {
        "logRotate": {
            "sizeThresholdMB": 1000,
            "timeThresholdHrs": 24,
            "numUncompressed": 5,
        },
        "systemLog": {
            "path": "/data/mongodb.log",
            "destination": "file",
            "logAppend": True,
        },
        "oplogSizeMB": 2048,
    },
    {
        "logRotate": {
            "sizeThresholdMB": 1000,
            "timeThresholdHrs": 24,
            "numUncompressed": 5,
        },
        "systemLog": {
            "path": "/data/mongodb.log",
            "destination": "file",
            "logAppend": True,
        },
        "oplogSizeMB": 2048,
    },
    {
        "logRotate": {
            "sizeThresholdMB": 2000,
            "timeThresholdHrs": 24,
            "numUncompressed": 3,
        },
        "systemLog": {
            "path": "/data/mongodb.log",
            "destination": "file",
            "logAppend": False,
        },
        "oplogSizeMB": None,  # absent on this member
    },
]


def _configure_ac_heterogeneous(namespace: str, om_tester: OMTester, vm_sts: dict, vm_service: dict, mdb_version: str):
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
                "roles": [{"role": "readWrite", "db": "myapp"}],
                "mechanisms": ["SCRAM-SHA-256"],
                "scramSha256Creds": {
                    "iterationCount": 15000,
                    "salt": "A0YIBVpnx7j8H2X4rSl0iU8whvg2u/odX49SGw==",
                    "serverKey": "pjgCbQ+C3LBmb0q6x7RkIsvI9DlJnHWVBf6YU5cLeUo=",
                    "storedKey": "nt65rpJuj/5TyaXGoJg+9BB4rhZPAXwDdHWJ+K8VtCs=",
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

    ac["processes"] = []
    ac["monitoringVersions"] = []
    ac["replicaSets"] = [{"_id": rs_name, "members": [], "protocolVersion": "1"}]

    for i in range(vm_sts["spec"]["replicas"]):
        hostname = f"{sts_name}-{i}.{svc_name}.{namespace}.svc.cluster.local"
        overrides = _MEMBER_OVERRIDES[i] if i < len(_MEMBER_OVERRIDES) else _MEMBER_OVERRIDES[0]

        ac["monitoringVersions"].append(
            {
                "hostname": hostname,
                "logPath": "/var/log/mongodb-mms-automation/monitoring-agent.log",
                "logRotate": {"sizeThresholdMB": 2000, "timeThresholdHrs": 48},
            }
        )

        args = {
            "net": {
                "port": 27017,
                "tls": {"mode": "disabled"},
                "compression": {"compressors": "zlib,zstd"},
                "maxIncomingConnections": 512,
            },
            "storage": {
                "dbPath": "/data/",
                "directoryPerDB": True,
                "wiredTiger": {
                    "engineConfig": {
                        "cacheSizeGB": 1,
                        "journalCompressor": "snappy",
                    },
                    "collectionConfig": {
                        "blockCompressor": "zstd",
                    },
                },
            },
            "operationProfiling": {
                "mode": "slowOp",
                "slowOpThresholdMs": 200,
            },
            "systemLog": overrides["systemLog"],
            "replication": {"replSetName": rs_name},
            "setParameter": {
                "authenticationMechanisms": "SCRAM-SHA-256",
                "connPoolMaxConnsPerHost": 800,
            },
        }
        if overrides["oplogSizeMB"] is not None:
            args["replication"]["oplogSizeMB"] = overrides["oplogSizeMB"]

        ac["processes"].append(
            {
                "version": mdb_version,
                "name": f"{sts_name}-{i}",
                "hostname": hostname,
                "logRotate": overrides["logRotate"],
                "authSchemaVersion": 5,
                "featureCompatibilityVersion": fcv_from_version(mdb_version),
                "processType": "mongod",
                "args2_6": args,
            }
        )

        ac["replicaSets"][0]["members"].append(
            {
                "_id": i + 100,
                "host": f"{sts_name}-{i}",
                "priority": 1,
                "votes": 1,
                "secondaryDelaySecs": 0,
                "hidden": False,
                "arbiterOnly": False,
            }
        )

    om_tester.api_put_automation_config(ac)


@fixture(scope="module")
def generated_cr_yaml(namespace: str) -> str:
    return run_migrate_generate(namespace, passwords=[APP_USER_PASSWORD])


@fixture(scope="module")
def generated_cr(generated_cr_yaml: str) -> dict:
    return next(yaml.safe_load_all(generated_cr_yaml))


@fixture(scope="module")
def mdb_migration(namespace: str, generated_cr: dict) -> MongoDB:
    resource = MongoDB(RS_NAME, namespace)
    if try_load(resource):
        return resource

    resource.backing_obj = generated_cr
    resource.backing_obj.setdefault("spec", {}).setdefault("additionalMongodConfig", {}).setdefault(
        "net", {}
    ).setdefault("tls", {})["mode"] = "disabled"
    resource.update()
    return resource


@fixture(scope="module")
def ac_before_migration(om_tester: OMTester) -> dict:
    return om_tester.api_get_automation_config()


@fixture(scope="module")
def ac_before_promote(om_tester: OMTester) -> dict:
    return om_tester.api_get_automation_config()


# ---------------------------------------------------------------------------
# Tests — CR structure assertions (run before apply)
# ---------------------------------------------------------------------------


@mark.e2e_vm_migration_generate_different_mongod_config
def test_deploy_vm(namespace: str, vm_sts, vm_service):
    def sts_is_ready():
        sts = get_statefulset(namespace, vm_sts["metadata"]["name"])
        return sts.status.ready_replicas == 3

    KubernetesTester.wait_until(sts_is_ready, timeout=300)


@mark.e2e_vm_migration_generate_different_mongod_config
def test_configure_ac(namespace: str, om_tester: OMTester, vm_sts, vm_service, custom_mdb_version):
    _configure_ac_heterogeneous(namespace, om_tester, vm_sts, vm_service, custom_mdb_version)


@mark.e2e_vm_migration_generate_different_mongod_config
def test_log_ac_after_vm_setup(om_tester: OMTester):
    log_automation_config(om_tester.api_get_automation_config(), label="after-vm-setup")


@mark.e2e_vm_migration_generate_different_mongod_config
def test_install_operator(operator: Operator):
    operator.assert_is_running()


@mark.e2e_vm_migration_generate_different_mongod_config
def test_logrotate_from_endpoint(generated_cr: dict):
    """logRotate is read from the deployment-level systemLogRotateConfig endpoint,
    so all fields are present regardless of per-process differences."""
    lr = generated_cr["spec"].get("agent", {}).get("mongod", {}).get("logRotate", {})
    assert lr.get("timeThresholdHrs") == 24, f"Expected timeThresholdHrs=24, got: {lr}"
    assert "sizeThresholdMB" in lr, f"Expected sizeThresholdMB from endpoint, got: {lr}"


@mark.e2e_vm_migration_generate_different_mongod_config
def test_monitoring_agent_logrotate(generated_cr: dict):
    """monitoringAgent.logRotate is read from the deployment-level monitoringAgentConfig endpoint,
    so per-host monitoringVersions overrides are not reflected."""
    ma = generated_cr["spec"].get("agent", {}).get("monitoringAgent", {})
    ma_lr = ma.get("logRotate", {})
    assert "sizeThresholdMB" in ma_lr, f"Expected sizeThresholdMB from endpoint, got: {ma_lr}"
    assert "timeThresholdHrs" in ma_lr, f"Expected timeThresholdHrs from endpoint, got: {ma_lr}"


@mark.e2e_vm_migration_generate_different_mongod_config
def test_systemlog_intersection(generated_cr: dict):
    """systemLog should keep destination + path (common). logAppend may appear as false
    (Go zero value) because the SystemLog struct uses a non-pointer bool field."""
    sl = generated_cr["spec"].get("agent", {}).get("mongod", {}).get("systemLog", {})
    assert sl.get("destination") == "file", f"Expected destination=file, got: {sl}"
    assert sl.get("path") == "/data/mongodb.log", f"Expected path, got: {sl}"


@mark.e2e_vm_migration_generate_different_mongod_config
def test_additional_config_common_fields(generated_cr: dict):
    """Compression and directoryPerDB are the same on all members — must be present."""
    amc = generated_cr["spec"].get("additionalMongodConfig", {})
    compressors = amc.get("net", {}).get("compression", {}).get("compressors")
    assert compressors == "zlib,zstd", f"Expected compressors 'zlib,zstd', got: {compressors}"
    assert amc.get("net", {}).get("maxIncomingConnections") == 512
    assert amc.get("storage", {}).get("directoryPerDB") is True


@mark.e2e_vm_migration_generate_different_mongod_config
def test_additional_config_wiredtiger_pass_through(generated_cr: dict):
    """WiredTiger fields pass through unchanged."""
    amc = generated_cr["spec"].get("additionalMongodConfig", {})
    wt = amc.get("storage", {}).get("wiredTiger", {})
    engine = wt.get("engineConfig", {})
    assert engine.get("cacheSizeGB") == 1, f"Expected cacheSizeGB=1, got: {engine}"
    assert engine.get("journalCompressor") == "snappy", f"Expected journalCompressor=snappy, got: {engine}"
    coll = wt.get("collectionConfig", {})
    assert coll.get("blockCompressor") == "zstd", f"Expected blockCompressor=zstd, got: {coll}"


@mark.e2e_vm_migration_generate_different_mongod_config
def test_additional_config_operation_profiling(generated_cr: dict):
    """operationProfiling passes through unchanged."""
    amc = generated_cr["spec"].get("additionalMongodConfig", {})
    op = amc.get("operationProfiling", {})
    assert op.get("mode") == "slowOp", f"Expected mode=slowOp, got: {op}"
    assert op.get("slowOpThresholdMs") == 200, f"Expected slowOpThresholdMs=200, got: {op}"


@mark.e2e_vm_migration_generate_different_mongod_config
def test_additional_config_set_parameter(generated_cr: dict):
    """setParameter fields pass through (excluding operator-managed authenticationMechanisms)."""
    amc = generated_cr["spec"].get("additionalMongodConfig", {})
    sp = amc.get("setParameter", {})
    assert sp.get("connPoolMaxConnsPerHost") == 800, f"Expected connPoolMaxConnsPerHost=800, got: {sp}"


@mark.e2e_vm_migration_generate_different_mongod_config
def test_additional_config_replication(generated_cr: dict):
    """additionalMongodConfig is taken from the source process; oplogSizeMB present on the source process appears in the CR."""
    amc = generated_cr["spec"].get("additionalMongodConfig", {})
    repl = amc.get("replication", {})
    assert repl.get("oplogSizeMB") == 2048, f"Expected oplogSizeMB=2048 from source process, got: {repl}"


@mark.e2e_vm_migration_generate_different_mongod_config
def test_security_present(generated_cr: dict):
    """Auth is enabled — security section must be present with SCRAM."""
    sec = generated_cr["spec"].get("security", {})
    auth = sec.get("authentication", {})
    assert auth.get("enabled") is True
    modes = auth.get("modes", [])
    assert "SCRAM" in modes, f"Expected SCRAM in modes, got: {modes}"


@mark.e2e_vm_migration_generate_different_mongod_config
def test_user_cr_emitted(generated_cr_yaml: str):
    docs = list(yaml.safe_load_all(generated_cr_yaml))
    user_docs = [d for d in docs if d and d.get("kind") == "MongoDBUser"]
    assert len(user_docs) == 1, f"Expected 1 user CR, got {len(user_docs)}"


@mark.e2e_vm_migration_generate_different_mongod_config
def test_external_members_structure(generated_cr: dict):
    ext = generated_cr["spec"]["externalMembers"]
    assert len(ext) == 3
    for em in ext:
        for key in ("processName", "hostname", "type", "replicaSetName"):
            assert key in em, f"Missing key {key!r} in externalMember: {em}"


# ---------------------------------------------------------------------------
# Lifecycle tests
# ---------------------------------------------------------------------------


@mark.e2e_vm_migration_generate_different_mongod_config
def test_migrate_vm_to_kubernetes(mdb_migration: MongoDB, ac_before_migration: dict):
    mdb_migration.assert_reaches_phase(Phase.Running, timeout=1200)


@mark.e2e_vm_migration_generate_different_mongod_config
def test_user_crs_reach_updated(generated_cr_yaml: str, namespace: str, mdb_migration: MongoDB, om_tester: OMTester):
    apply_user_crs_and_verify_ac(generated_cr_yaml, namespace, om_tester)


@mark.e2e_vm_migration_generate_different_mongod_config
def test_log_ac_after_migration(om_tester: OMTester, ac_before_migration: dict):
    ac_after = om_tester.api_get_automation_config()
    log_automation_config(ac_after, label="after-migration")
    log_automation_config_diff(ac_before_migration, ac_after)


@mark.e2e_vm_migration_generate_different_mongod_config
def test_promote_and_prune(mdb_migration: MongoDB, vm_sts, ac_before_promote: dict):
    try_load(mdb_migration)
    for i in range(vm_sts["spec"]["replicas"]):
        mdb_migration["spec"]["memberConfig"][i]["priority"] = "1"
        mdb_migration["spec"]["memberConfig"][i]["votes"] = 1
        mdb_migration.update()
        mdb_migration.assert_reaches_phase(Phase.Running, timeout=1200)

        mdb_migration["spec"]["externalMembers"].pop()
        mdb_migration.update()
        mdb_migration.assert_reaches_phase(Phase.Running, timeout=1200)


@mark.e2e_vm_migration_generate_different_mongod_config
def test_log_ac_after_promote(om_tester: OMTester, ac_before_promote: dict):
    ac_after = om_tester.api_get_automation_config()
    log_automation_config(ac_after, label="after-promote")
    log_automation_config_diff(ac_before_promote, ac_after)


@mark.e2e_vm_migration_generate_different_mongod_config
def test_password_rotation_keeps_migrated_flag(generated_cr_yaml: str, namespace: str, om_tester: OMTester):
    rotate_password_and_verify(generated_cr_yaml, namespace, om_tester)


@mark.e2e_vm_migration_generate_different_mongod_config
def test_log_ac_end_to_end(om_tester: OMTester, ac_before_migration: dict):
    ac_after = om_tester.api_get_automation_config()
    log_automation_config(ac_after, label="final")
    log_automation_config_diff(ac_before_migration, ac_after)
