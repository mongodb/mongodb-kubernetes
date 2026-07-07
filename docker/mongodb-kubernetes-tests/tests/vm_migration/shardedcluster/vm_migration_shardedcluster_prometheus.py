"""
VM migration for a sharded cluster with Prometheus metrics enabled.

Mirrors vm_migration_replicaset_prometheus.py adapted for sharded cluster:
two StatefulSets (mongod and mongos), config server promote/prune, and
mongos-tester for connectivity checks.
"""

import base64
import hashlib
import os

from kubetester import create_or_update_secret, get_statefulset, try_load
from kubetester.http import get_retriable_session
from kubetester.kubetester import KubernetesTester, ensure_ent_version, skip_if_local
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

PROM_USER = "prom-user"
PROM_PASSWORD = "prom-password"
PROM_PORT = 9216
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


def _configure_ac(namespace: str, om_tester: OMTester, mdb_version: str) -> None:
    ac_existing = om_tester.api_get_automation_config()
    if len(ac_existing.get("processes", [])) > 0:
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
    )
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
    om_tester.api_put_automation_config(ac)


@fixture(scope="module")
def generated_cr_yaml(namespace: str) -> str:
    create_or_update_secret(namespace, PROMETHEUS_PASSWORD_SECRET, {"password": PROM_PASSWORD})
    return run_generate_cr(
        namespace,
        resource_name_override=MDB_RESOURCE_NAME,
        prometheus_secret_name=PROMETHEUS_PASSWORD_SECRET,
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


def _assert_metrics_served(mdb_migration: MongoDB, namespace: str) -> None:
    session = get_retriable_session("http", tls_verify=False)
    for i in range(mdb_migration["spec"].get("mongodsPerShardCount", 3)):
        url = (
            f"http://{mdb_migration.name}-0-{i}.{mdb_migration.name}-sh."
            f"{namespace}.svc.cluster.local:{PROM_PORT}/metrics"
        )
        unauth = session.get(url)
        assert unauth.status_code == 401, f"{url} without auth returned {unauth.status_code}, expected 401"
        resp = session.get(url, auth=(PROM_USER, PROM_PASSWORD))
        assert resp.status_code == 200, f"{url} returned {resp.status_code}"
        assert "# HELP" in resp.text


@mark.e2e_vm_migration_shardedcluster_prometheus
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


@mark.e2e_vm_migration_shardedcluster_prometheus
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
    _configure_ac(namespace, om_tester, ensure_ent_version(custom_mdb_version))
    om_tester.wait_agents_ready(timeout=600)


@mark.e2e_vm_migration_shardedcluster_prometheus
def test_install_operator(operator: Operator):
    operator.assert_is_running()


@mark.e2e_vm_migration_shardedcluster_prometheus
def test_insert_migration_data(namespace: str):
    insert_migration_data(vm_mongos_tester(MONGOS_STS_NAME, MONGOS_SVC_NAME, namespace))


@mark.e2e_vm_migration_shardedcluster_prometheus
def test_common_generated_cr_shape(generated_cr: dict):
    assert_common_generated_sharded_cr_shape(
        generated_cr,
        expected_config_count=MIN_VM_CONFIGSRV,
        expected_shard_count=MIN_VM_SHARD,
        expected_mongos_count=MIN_VM_MONGOS,
    )


@mark.e2e_vm_migration_shardedcluster_prometheus
def test_prometheus_in_cr(generated_cr: dict):
    prom = generated_cr["spec"]["prometheus"]
    assert prom["username"] == PROM_USER
    assert prom["passwordSecretRef"]["name"] == PROMETHEUS_PASSWORD_SECRET
    assert prom.get("port", PROM_PORT) == PROM_PORT


@mark.e2e_vm_migration_shardedcluster_prometheus
def test_migration_dry_run_connectivity_passes(mdb_migration: MongoDB):
    run_migration_dry_run_connectivity_passes(mdb_migration)


@mark.e2e_vm_migration_shardedcluster_prometheus
def test_migrate_vm_to_kubernetes(mdb_migration: MongoDB):
    mdb_migration.assert_reaches_phase(Phase.Running, timeout=1800)


@mark.e2e_vm_migration_shardedcluster_prometheus
def test_max_voting_members_validation(mdb_migration: MongoDB):
    assert_max_voting_members_validation(mdb_migration)


@mark.e2e_vm_migration_shardedcluster_prometheus
@skip_if_local()
def test_prometheus_endpoint_after_migration(mdb_migration: MongoDB, namespace: str):
    _assert_metrics_served(mdb_migration, namespace)


@mark.e2e_vm_migration_shardedcluster_prometheus
def test_migration_data_exists_after_migration(mdb_migration: MongoDB):
    assert_migration_data_exists(mdb_migration.tester())


@mark.e2e_vm_migration_shardedcluster_prometheus
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


@mark.e2e_vm_migration_shardedcluster_prometheus
def test_prune_shard(mdb_migration: MongoDB, om_tester: OMTester):
    promote_and_prune_shard(mdb_migration, om_tester, VM_SHARD_RS_NAME, VM_MONGOS_NAME)


@mark.e2e_vm_migration_shardedcluster_prometheus
def test_prune_mongos(mdb_migration: MongoDB):
    try_load(mdb_migration)
    mongos_external = [m for m in mdb_migration["spec"]["externalMembers"] if m["type"] == "mongos"]
    for m in mongos_external:
        mdb_migration["spec"]["externalMembers"].remove(m)
    mdb_migration.update()
    mdb_migration.assert_reaches_phase(Phase.Running)


@mark.e2e_vm_migration_shardedcluster_prometheus
def test_connection_string_after_full_migration(mdb_migration: MongoDB):
    assert_connection_string_after_full_sharded_migration(mdb_migration)


@mark.e2e_vm_migration_shardedcluster_prometheus
def test_process_names(om_tester: OMTester, mdb_migration: MongoDB):
    assert_k8s_sharded_process_names(om_tester, mdb_migration)


@mark.e2e_vm_migration_shardedcluster_prometheus
@skip_if_local()
def test_prometheus_endpoint_after_promote(mdb_migration: MongoDB, namespace: str):
    _assert_metrics_served(mdb_migration, namespace)


@mark.e2e_vm_migration_shardedcluster_prometheus
def test_migration_data_exists_after_promote(mdb_migration: MongoDB):
    assert_migration_data_exists(mdb_migration.tester())
