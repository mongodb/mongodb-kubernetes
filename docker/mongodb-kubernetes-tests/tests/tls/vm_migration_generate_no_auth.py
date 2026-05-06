"""
VM migration test using kubectl-mongodb migrate WITHOUT authentication.

Covers the auth-disabled code path in the migration tool, which none of the
other generate-based tests exercise.  Verifies:
  - The generated CR has NO spec.security section
  - No MongoDBUser CRs are emitted
  - externalMembers are properly structured (object form)
  - generated CR contains no memberConfig (added by the test fixture for migration)
  - additionalMongodConfig carries the expected fields
  - Full promote-and-prune lifecycle reaches Phase.Running
"""

import yaml
from kubetester import get_statefulset, try_load
from kubetester.kubetester import KubernetesTester, ensure_ent_version, fcv_from_version, skip_if_local
from kubetester.mongodb import MongoDB
from kubetester.omtester import OMContext, OMTester
from kubetester.operator import Operator
from kubetester.phase import Phase
from pytest import fixture, mark
from tests.tls.vm_migration_dry_run import run_migration_dry_run_connectivity_passes
from tests.tls.vm_migration_helpers import (
    assert_migration_dry_run_annotation,
    deploy_vm_service,
    deploy_vm_statefulset,
    promote_and_prune,
    run_generate_cr,
    vm_replica_set_tester,
)

RS_NAME = "vm-mongodb-rs"


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


def _configure_ac_no_auth(namespace: str, om_tester: OMTester, vm_sts: dict, vm_service: dict, mdb_version: str):
    """Set up a replica set with auth DISABLED, custom port 27018, and compression."""
    mdb_version = ensure_ent_version(mdb_version)
    ac = om_tester.api_get_automation_config()
    if len(ac["processes"]) > 0:
        return

    sts_name = vm_sts["metadata"]["name"]
    svc_name = vm_service["metadata"]["name"]
    rs_name = f"{sts_name}-rs"

    ac["auth"] = {"disabled": True, "authoritativeSet": False}

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

        ac["processes"].append(
            {
                "version": mdb_version,
                "name": f"{sts_name}-{i}",
                "hostname": hostname,
                "logRotate": {"sizeThresholdMB": 1000, "timeThresholdHrs": 24},
                "authSchemaVersion": 5,
                "featureCompatibilityVersion": fcv_from_version(mdb_version),
                "processType": "mongod",
                "args2_6": {
                    "net": {
                        "port": 27017,
                        "tls": {"mode": "disabled"},
                        "compression": {"compressors": "snappy,zstd"},
                    },
                    "storage": {
                        "dbPath": "/data/",
                        "directoryPerDB": True,
                    },
                    "systemLog": {
                        "path": "/data/mongodb.log",
                        "destination": "file",
                    },
                    "replication": {"replSetName": rs_name},
                },
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
    """Raw stdout from migrate (no SCRAM users, so no secrets needed)."""
    return run_generate_cr(namespace)


@fixture(scope="module")
def generated_cr(generated_cr_yaml: str) -> dict:
    """Parsed first YAML document from the generate output."""
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
    # The generated CR starts with members=0 and no memberConfig.
    # Set members to match the VM replica count and add draining memberConfig.
    num_members = len(generated_cr["spec"].get("externalMembers", []))
    resource.backing_obj["spec"]["members"] = num_members
    resource.backing_obj["spec"]["memberConfig"] = [{"votes": 0, "priority": "0"} for _ in range(num_members)]
    resource.update()
    return resource


# ---------------------------------------------------------------------------
# Tests
# ---------------------------------------------------------------------------


@mark.e2e_vm_migration_generate_no_auth
def test_deploy_vm(namespace: str, vm_sts, vm_service):
    def sts_is_ready():
        sts = get_statefulset(namespace, vm_sts["metadata"]["name"])
        return sts.status.ready_replicas == 3

    KubernetesTester.wait_until(sts_is_ready, timeout=300)


@mark.e2e_vm_migration_generate_no_auth
def test_configure_ac(namespace: str, om_tester: OMTester, vm_sts, vm_service, custom_mdb_version):
    _configure_ac_no_auth(namespace, om_tester, vm_sts, vm_service, custom_mdb_version)


@mark.e2e_vm_migration_generate_no_auth
@skip_if_local()
def test_connectivity_before_migration(namespace: str):
    """Replica set is reachable without authentication before migration."""
    vm_replica_set_tester(namespace).assert_connectivity()


@mark.e2e_vm_migration_generate_no_auth
def test_install_operator(operator: Operator):
    operator.assert_is_running()


# --- Generated CR checks (all run immediately after generation, before any lifecycle test) ---


@mark.e2e_vm_migration_generate_no_auth
def test_migration_dry_run_annotation_present(generated_cr_yaml: str):
    """Generated MongoDB CR must carry the migration-dry-run annotation."""
    assert_migration_dry_run_annotation(generated_cr_yaml)


@mark.e2e_vm_migration_generate_no_auth
def test_no_security_in_cr(generated_cr: dict):
    """Auth is disabled -- the generated CR must not contain a security section."""
    spec = generated_cr.get("spec", {})
    assert (
        "security" not in spec
    ), f"Expected no security section for auth-disabled deployment, got: {spec.get('security')}"


@mark.e2e_vm_migration_generate_no_auth
def test_no_user_crs_emitted(generated_cr_yaml: str):
    """Without auth, migrate must not emit any MongoDBUser documents."""
    docs = list(yaml.safe_load_all(generated_cr_yaml))
    user_docs = [d for d in docs if d and d.get("kind") == "MongoDBUser"]
    assert len(user_docs) == 0, f"Expected 0 user CRs, got {len(user_docs)}"


@mark.e2e_vm_migration_generate_no_auth
def test_external_members_structure(generated_cr: dict):
    """Each externalMember must be an object with processName, hostname, type, replicaSetName."""
    ext = generated_cr["spec"]["externalMembers"]
    assert len(ext) == 3, f"Expected 3 external members, got {len(ext)}"
    for em in ext:
        assert isinstance(em, dict), f"externalMember should be a dict, got {type(em)}"
        for key in ("processName", "hostname", "type", "replicaSetName"):
            assert key in em, f"Missing key {key!r} in externalMember: {em}"
        assert em["type"] == "mongod"


@mark.e2e_vm_migration_generate_no_auth
def test_members_not_set(generated_cr: dict):
    """Generated CR omits members (operator default applies). Customers set it when expanding."""
    assert (
        "memberConfig" not in generated_cr["spec"]
    ), "Generated CR should not contain memberConfig. Customers set it when expanding."


@mark.e2e_vm_migration_generate_no_auth
def test_additional_mongod_config(generated_cr: dict):
    """additionalMongodConfig must reflect the net.compression.compressors and storage settings."""
    amc = generated_cr["spec"].get("additionalMongodConfig", {})
    assert (
        amc.get("net", {}).get("compression", {}).get("compressors") == "snappy,zstd"
    ), f"Expected compressors 'snappy,zstd', got: {amc}"
    assert amc.get("storage", {}).get("directoryPerDB") is True, f"Expected directoryPerDB=true, got: {amc}"


@mark.e2e_vm_migration_generate_no_auth
def test_version_set(generated_cr: dict, custom_mdb_version: str):
    """spec.version must match the MongoDB version used in the AC."""
    assert generated_cr["spec"]["version"] == ensure_ent_version(custom_mdb_version)


@mark.e2e_vm_migration_generate_no_auth
def test_agent_config(generated_cr: dict):
    """Agent config must include logRotate and systemLog from the (uniform) process config."""
    agent = generated_cr["spec"].get("agent", {}).get("mongod", {})
    lr = agent.get("logRotate", {})
    assert (
        lr.get("sizeThresholdMB") == "1000" or lr.get("sizeThresholdMB") == 1000
    ), f"Expected logRotate.sizeThresholdMB=1000, got: {lr}"
    sl = agent.get("systemLog", {})
    assert sl.get("destination") == "file", f"Expected systemLog.destination=file, got: {sl}"
    assert sl.get("path") == "/data/mongodb.log", f"Expected systemLog.path, got: {sl}"


# --- Lifecycle tests ---


@mark.e2e_vm_migration_generate_no_auth
def test_migration_dry_run_connectivity_passes(mdb_migration: MongoDB):
    """Operator validates connectivity to all externalMembers, then the annotation is removed."""
    run_migration_dry_run_connectivity_passes(mdb_migration)


@mark.e2e_vm_migration_generate_no_auth
def test_migrate_vm_to_kubernetes(mdb_migration: MongoDB):
    mdb_migration.assert_reaches_phase(Phase.Running, timeout=1200)


@mark.e2e_vm_migration_generate_no_auth
@skip_if_local()
def test_connectivity_after_migration(mdb_migration: MongoDB):
    """Replica set remains reachable without authentication after migration."""
    mdb_migration.tester(use_ssl=False).assert_connectivity()


@mark.e2e_vm_migration_generate_no_auth
def test_promote_and_prune(mdb_migration: MongoDB, vm_sts):
    promote_and_prune(mdb_migration, vm_sts)


@mark.e2e_vm_migration_generate_no_auth
@skip_if_local()
def test_connectivity_after_promote(mdb_migration: MongoDB):
    """Replica set remains reachable without authentication after promote-and-prune."""
    mdb_migration.tester(use_ssl=False).assert_connectivity()
