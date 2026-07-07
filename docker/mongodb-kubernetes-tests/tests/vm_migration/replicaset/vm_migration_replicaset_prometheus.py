"""
VM migration from a generated MongoDB resource with Prometheus metrics enabled.

This test configures VM members in Ops Manager with an HTTP Prometheus endpoint (auth disabled on the
database itself), runs kubectl-mongodb migrate-to-mck, applies the generated resources, and verifies
that spec.prometheus is carried over and the operator-managed pods serve authenticated metrics.
"""

import base64
import hashlib
import os

from kubetester import create_or_update_secret, get_statefulset
from kubetester.http import get_retriable_session
from kubetester.kubetester import KubernetesTester, ensure_ent_version, fcv_from_version, skip_if_local
from kubetester.mongodb import MongoDB
from kubetester.omtester import OMContext, OMTester
from kubetester.operator import Operator
from kubetester.phase import Phase
from pytest import fixture, mark
from tests.vm_migration.vm_migration_common_helper import (
    assert_max_voting_members_validation,
    assert_migration_data_exists,
    generated_mongodb_doc,
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
PROM_USER = "prom-user"
PROM_PASSWORD = "prom-password"
PROM_PORT = 9216
# The migrate tool wires spec.prometheus.passwordSecretRef to this fixed Secret name.
PROMETHEUS_PASSWORD_SECRET = "prometheus-password"

_PBKDF2_ITERATIONS = 256
_PBKDF2_KEY_LENGTH = 32
_PROM_SALT_SIZE = 8


def _build_prometheus_hash(password: str, salt: bytes | None = None) -> tuple[str, str]:
    if salt is None:
        salt = os.urandom(_PROM_SALT_SIZE)
    dk = hashlib.pbkdf2_hmac("sha256", password.encode("utf-8"), salt, _PBKDF2_ITERATIONS, dklen=_PBKDF2_KEY_LENGTH)
    return base64.b64encode(dk).decode("utf-8"), base64.b64encode(salt).decode("utf-8")


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
    """Auth disabled on the database, with an HTTP Prometheus endpoint on port 9216."""
    mdb_version = ensure_ent_version(mdb_version)
    ac = om_tester.api_get_automation_config()
    if len(ac["processes"]) > 0:
        return

    sts_name = vm_sts["metadata"]["name"]
    svc_name = vm_service["metadata"]["name"]
    rs_name = f"{sts_name}-rs"

    ac["auth"] = {"disabled": True, "authoritativeSet": False}
    prom_hash, prom_salt = _build_prometheus_hash(PROM_PASSWORD)
    ac["prometheus"] = {
        "enabled": True,
        "username": PROM_USER,
        "passwordHash": prom_hash,
        "passwordSalt": prom_salt,
        "scheme": "http",
        "listenAddress": f"0.0.0.0:{PROM_PORT}",
        "metricsPath": "/metrics",
    }

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
                    "net": {"port": 27017, "tls": {"mode": "disabled"}},
                    "storage": {"dbPath": "/data/"},
                    "systemLog": {"path": "/data/mongodb.log", "destination": "file"},
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
    # The tool validates the Prometheus password Secret exists at generate time and references it by name.
    create_or_update_secret(namespace, PROMETHEUS_PASSWORD_SECRET, {"password": PROM_PASSWORD})
    return run_generate_cr(namespace, prometheus_secret_name=PROMETHEUS_PASSWORD_SECRET)


@fixture(scope="module")
def generated_cr(generated_cr_yaml: str) -> dict:
    return generated_mongodb_doc(generated_cr_yaml)


@fixture(scope="module")
def mdb_migration(namespace: str, generated_cr: dict) -> MongoDB:
    return apply_generated_mongodb_resource(namespace, generated_cr, customer_sets_disabled_tls_mode=True)


def _assert_metrics_served(mdb_migration: MongoDB, namespace: str):
    """Every operator-managed pod serves authenticated Prometheus metrics over HTTP on PROM_PORT.

    Asserts both that the migrated credentials work (200 + metrics) and that auth is enforced
    (an unauthenticated request is rejected).
    """
    session = get_retriable_session("http", tls_verify=False)
    for i in range(mdb_migration.get_members()):
        url = f"http://{mdb_migration.name}-{i}.{mdb_migration.name}-svc.{namespace}.svc.cluster.local:{PROM_PORT}/metrics"

        unauth = session.get(url)
        assert unauth.status_code == 401, f"{url} without auth returned {unauth.status_code}, expected 401"

        resp = session.get(url, auth=(PROM_USER, PROM_PASSWORD))
        assert resp.status_code == 200, f"{url} returned {resp.status_code}"
        assert "# HELP" in resp.text, f"{url} did not return Prometheus metrics"
        assert "mongodb" in resp.text.lower(), f"{url} did not return MongoDB metrics"


# Test flow


@mark.e2e_vm_migration_replicaset_prometheus
def test_deploy_vm(namespace: str, vm_sts, vm_service):
    def sts_is_ready():
        sts = get_statefulset(namespace, vm_sts["metadata"]["name"])
        return sts.status.ready_replicas == vm_sts["spec"]["replicas"]

    KubernetesTester.wait_until(sts_is_ready, timeout=300)


@mark.e2e_vm_migration_replicaset_prometheus
def test_configure_ac(namespace: str, om_tester: OMTester, vm_sts, vm_service, custom_mdb_version):
    _configure_ac(namespace, om_tester, vm_sts, vm_service, custom_mdb_version)
    om_tester.wait_agents_ready(timeout=600)


@mark.e2e_vm_migration_replicaset_prometheus
def test_install_operator(operator: Operator):
    operator.assert_is_running()


@mark.e2e_vm_migration_replicaset_prometheus
def test_insert_migration_data(namespace: str):
    insert_migration_data(vm_replica_set_tester(namespace))


# Generated CR checks


@mark.e2e_vm_migration_replicaset_prometheus
def test_common_generated_cr_shape(generated_cr_yaml: str, generated_cr: dict, vm_sts: dict):
    assert_common_generated_cr_shape(generated_cr_yaml, generated_cr, vm_sts["spec"]["replicas"])


@mark.e2e_vm_migration_replicaset_prometheus
def test_prometheus_in_cr(generated_cr: dict):
    prom = generated_cr["spec"]["prometheus"]
    assert prom["username"] == PROM_USER
    assert prom["passwordSecretRef"]["name"] == PROMETHEUS_PASSWORD_SECRET
    assert prom.get("port", PROM_PORT) == PROM_PORT


# Lifecycle checks


@mark.e2e_vm_migration_replicaset_prometheus
def test_migration_dry_run_connectivity_passes(mdb_migration: MongoDB):
    run_migration_dry_run_connectivity_passes(mdb_migration)


@mark.e2e_vm_migration_replicaset_prometheus
def test_migrate_vm_to_kubernetes(mdb_migration: MongoDB):
    mdb_migration.assert_reaches_phase(Phase.Running, timeout=1200)
    assert_connection_string_contains_current_hosts(mdb_migration)


@mark.e2e_vm_migration_replicaset_prometheus
def test_max_voting_members_validation(mdb_migration: MongoDB):
    assert_max_voting_members_validation(mdb_migration)


@mark.e2e_vm_migration_replicaset_prometheus
@skip_if_local()
def test_prometheus_endpoint_after_migration(mdb_migration: MongoDB, namespace: str):
    _assert_metrics_served(mdb_migration, namespace)


@mark.e2e_vm_migration_replicaset_prometheus
def test_migration_data_exists_after_migration(mdb_migration: MongoDB):
    assert_migration_data_exists(mdb_migration.tester(use_ssl=False))


@mark.e2e_vm_migration_replicaset_prometheus
def test_promote_and_prune(mdb_migration: MongoDB, vm_sts):
    promote_and_prune(mdb_migration, vm_sts)


@mark.e2e_vm_migration_replicaset_prometheus
def test_connection_string_after_full_migration(mdb_migration: MongoDB):
    assert_connection_string_after_full_migration(mdb_migration)


@mark.e2e_vm_migration_replicaset_prometheus
def test_process_names(om_tester: OMTester, mdb_migration: MongoDB):
    assert_k8s_process_names(om_tester, mdb_migration)


@mark.e2e_vm_migration_replicaset_prometheus
@skip_if_local()
def test_prometheus_endpoint_after_promote(mdb_migration: MongoDB, namespace: str):
    _assert_metrics_served(mdb_migration, namespace)


@mark.e2e_vm_migration_replicaset_prometheus
def test_migration_data_exists_after_promote(mdb_migration: MongoDB):
    assert_migration_data_exists(mdb_migration.tester(use_ssl=False))
