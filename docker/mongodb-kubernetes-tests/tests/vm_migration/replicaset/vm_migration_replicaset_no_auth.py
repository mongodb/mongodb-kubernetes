"""
VM migration from a generated MongoDB resource with authentication disabled.

This test configures VM members in Ops Manager, runs kubectl-mongodb migrate-to-mck,
applies the generated resources, and verifies dry-run validation, data continuity,
connection strings, process names, and the promote and prune flow.
"""

from kubetester import get_statefulset
from kubetester.kubetester import KubernetesTester, ensure_ent_version, fcv_from_version, skip_if_local
from kubetester.mongodb import MongoDB
from kubetester.mongotester import MongoDBBackgroundTester
from kubetester.omtester import OMContext, OMTester
from kubetester.operator import Operator
from kubetester.phase import Phase
from pytest import fixture, mark
from tests.vm_migration.vm_migration_common_helper import (
    assert_max_voting_members_validation,
    assert_migration_data_exists,
    generated_mongodb_doc,
    generated_user_docs,
    insert_migration_data,
    run_generate_cr,
)
from tests.vm_migration.vm_migration_dry_run import run_migration_dry_run_connectivity_passes
from tests.vm_migration.vm_migration_replicaset_helper import (
    apply_generated_mongodb_resource,
    assert_common_generated_cr_shape,
    assert_connection_string_after_full_migration,
    assert_connection_string_contains_current_hosts,
    assert_k8s_process_names,
    deploy_vm_service,
    deploy_vm_statefulset,
    promote_and_prune,
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
    return generated_mongodb_doc(generated_cr_yaml)


@fixture(scope="module")
def mdb_migration(namespace: str, generated_cr: dict) -> MongoDB:
    return apply_generated_mongodb_resource(namespace, generated_cr, customer_sets_disabled_tls_mode=True)


@fixture(scope="module")
def mdb_health_checker(mdb_migration: MongoDB) -> MongoDBBackgroundTester:
    return MongoDBBackgroundTester(mdb_migration.tester(use_ssl=False))


# Test flow


@mark.e2e_vm_migration_replicaset_no_auth
def test_deploy_vm(namespace: str, vm_sts, vm_service):
    def sts_is_ready():
        sts = get_statefulset(namespace, vm_sts["metadata"]["name"])
        return sts.status.ready_replicas == vm_sts["spec"]["replicas"]

    KubernetesTester.wait_until(sts_is_ready, timeout=300)


@mark.e2e_vm_migration_replicaset_no_auth
def test_configure_ac(namespace: str, om_tester: OMTester, vm_sts, vm_service, custom_mdb_version):
    _configure_ac_no_auth(namespace, om_tester, vm_sts, vm_service, custom_mdb_version)
    om_tester.wait_agents_ready(timeout=600)


@mark.e2e_vm_migration_replicaset_no_auth
@skip_if_local()
def test_connectivity_before_migration(namespace: str):
    """Replica set is reachable without authentication before migration."""
    vm_replica_set_tester(namespace).assert_connectivity()


@mark.e2e_vm_migration_replicaset_no_auth
def test_install_operator(operator: Operator):
    operator.assert_is_running()


@mark.e2e_vm_migration_replicaset_no_auth
def test_insert_migration_data(namespace: str):
    insert_migration_data(vm_replica_set_tester(namespace))


# Generated CR checks


@mark.e2e_vm_migration_replicaset_no_auth
def test_common_generated_cr_shape(generated_cr_yaml: str, generated_cr: dict, vm_sts: dict):
    assert_common_generated_cr_shape(generated_cr_yaml, generated_cr, vm_sts["spec"]["replicas"])


@mark.e2e_vm_migration_replicaset_no_auth
def test_no_security_in_cr(generated_cr: dict):
    """Auth is disabled -- the generated CR must not contain a security section."""
    spec = generated_cr.get("spec", {})
    assert (
        "security" not in spec
    ), f"Expected no security section for auth-disabled deployment, got: {spec.get('security')}"


@mark.e2e_vm_migration_replicaset_no_auth
def test_no_user_crs_emitted(generated_cr_yaml: str):
    """Without auth, migrate must not emit any MongoDBUser documents."""
    user_docs = generated_user_docs(generated_cr_yaml)
    assert len(user_docs) == 0, f"Expected 0 user CRs, got {len(user_docs)}"


@mark.e2e_vm_migration_replicaset_no_auth
def test_additional_mongod_config(generated_cr: dict):
    """additionalMongodConfig must reflect the net.compression.compressors and storage settings."""
    amc = generated_cr["spec"].get("additionalMongodConfig", {})
    assert (
        amc.get("net", {}).get("compression", {}).get("compressors") == "snappy,zstd"
    ), f"Expected compressors 'snappy,zstd', got: {amc}"
    assert amc.get("storage", {}).get("directoryPerDB") is True, f"Expected directoryPerDB=true, got: {amc}"


@mark.e2e_vm_migration_replicaset_no_auth
def test_version_set(generated_cr: dict, custom_mdb_version: str):
    """spec.version must match the MongoDB version used in the AC."""
    assert generated_cr["spec"]["version"] == ensure_ent_version(custom_mdb_version)


@mark.e2e_vm_migration_replicaset_no_auth
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


# Lifecycle checks


@mark.e2e_vm_migration_replicaset_no_auth
def test_migration_dry_run_connectivity_passes(mdb_migration: MongoDB):
    """Operator validates connectivity to all externalMembers, then the annotation is removed."""
    run_migration_dry_run_connectivity_passes(mdb_migration)


@mark.e2e_vm_migration_replicaset_no_auth
def test_migrate_vm_to_kubernetes(mdb_migration: MongoDB):
    mdb_migration.assert_reaches_phase(Phase.Running, timeout=1200)
    assert_connection_string_contains_current_hosts(mdb_migration)


@mark.e2e_vm_migration_replicaset_no_auth
def test_max_voting_members_validation(mdb_migration: MongoDB):
    assert_max_voting_members_validation(mdb_migration)


@mark.e2e_vm_migration_replicaset_no_auth
@skip_if_local()
def test_connectivity_after_migration(mdb_migration: MongoDB):
    """Replica set remains reachable without authentication after migration."""
    mdb_migration.tester(use_ssl=False).assert_connectivity()


@mark.e2e_vm_migration_replicaset_no_auth
def test_migration_data_exists_after_migration(mdb_migration: MongoDB):
    assert_migration_data_exists(mdb_migration.tester(use_ssl=False))


@mark.e2e_vm_migration_replicaset_no_auth
@skip_if_local()
def test_start_background_health_checker(mdb_health_checker: MongoDBBackgroundTester):
    mdb_health_checker.start()


@mark.e2e_vm_migration_replicaset_no_auth
def test_promote_and_prune(mdb_migration: MongoDB, vm_sts):
    promote_and_prune(mdb_migration, vm_sts)


@mark.e2e_vm_migration_replicaset_no_auth
def test_connection_string_after_full_migration(mdb_migration: MongoDB):
    assert_connection_string_after_full_migration(mdb_migration)


@mark.e2e_vm_migration_replicaset_no_auth
def test_process_names(om_tester: OMTester, mdb_migration: MongoDB):
    assert_k8s_process_names(om_tester, mdb_migration)


@mark.e2e_vm_migration_replicaset_no_auth
@skip_if_local()
def test_mongodb_reachable_during_promote_and_prune(mdb_health_checker: MongoDBBackgroundTester):
    mdb_health_checker.assert_healthiness()
    mdb_health_checker.stop()


@mark.e2e_vm_migration_replicaset_no_auth
@skip_if_local()
def test_connectivity_after_promote(mdb_migration: MongoDB):
    """Replica set remains reachable without authentication after promote and prune."""
    mdb_migration.tester(use_ssl=False).assert_connectivity()


@mark.e2e_vm_migration_replicaset_no_auth
def test_migration_data_exists_after_promote(mdb_migration: MongoDB):
    assert_migration_data_exists(mdb_migration.tester(use_ssl=False))
