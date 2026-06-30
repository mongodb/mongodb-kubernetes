"""
VM migration for a sharded cluster with SCRAM-SHA-256 and TLS.

Mirrors vm_migration_replicaset_scram_sha256_tls.py adapted for sharded cluster:
two StatefulSets (mongod and mongos) with TLS cert mounts, config server
promote/prune, and mongos-tester for connectivity checks.
"""

import os
import ssl

from kubetester import create_or_update_configmap, create_or_update_secret, get_statefulset, read_secret, try_load
from kubetester.certs import ISSUER_CA_NAME, create_mongodb_tls_certs
from kubetester.kubetester import KubernetesTester, ensure_ent_version, skip_if_local
from kubetester.mongodb import MongoDB
from kubetester.mongotester import MongoDBBackgroundTester, with_scram
from kubetester.omtester import OMContext, OMTester
from kubetester.operator import Operator
from kubetester.phase import Phase
from kubetester.scram import build_sha256_creds
from pytest import fixture, mark
from tests.vm_migration.vm_migration_dry_run import (
    create_wrong_ca_configmap,
    run_migration_dry_run_connectivity_passes,
    run_wrong_ca_dry_run_fails_then_passes,
)
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
    rotate_password_and_verify,
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

MONGOD_CERT_SECRET = "vm-sharded-mongod-cert"
MONGOS_CERT_SECRET = "vm-sharded-mongos-cert"
MONGOD_TLS_PEM_SECRET = "vm-sharded-mongod-tls-pem"
MONGOS_TLS_PEM_SECRET = "vm-sharded-mongos-tls-pem"
CERT_SECRET_PREFIX = "mdb"
OPERATOR_CERT_SECRET = f"{CERT_SECRET_PREFIX}-{MDB_RESOURCE_NAME}-cert"
VM_AGENT_OM_CA_PATH = "/etc/mongodb-mms-ca/ca.pem"
VM_OM_CA_CONFIGMAP_NAME = "vm-sharded-om-ca"
WRONG_CA_NAME = "wrong-issuer-ca-sharded-tls"

APP_USER_PASSWORD = "tlsShardedAppUser123!"


def _get_ca_bundle_content() -> str:
    paths = ssl.get_default_verify_paths()
    path = paths.cafile or paths.openssl_cafile or "/etc/ssl/certs/ca-certificates.crt"
    if not os.path.exists(path):
        raise FileNotFoundError(f"No system CA bundle found at {path}")
    with open(path, "r") as f:
        return f.read()


@fixture(scope="module")
def om_tester(namespace: str) -> OMTester:
    config_map = KubernetesTester.read_configmap(namespace, "my-project")
    secret = KubernetesTester.read_secret(namespace, "my-credentials")
    tester = OMTester(OMContext.build_from_config_map_and_secret(config_map, secret))
    tester.ensure_agent_api_key()
    return tester


@fixture(scope="module")
def vm_mongod_server_certs(issuer: str, namespace: str):
    return create_mongodb_tls_certs(
        ISSUER_CA_NAME,
        namespace,
        MONGOD_STS_NAME,
        MONGOD_CERT_SECRET,
        replicas=CONFIG_SERVER_COUNT + SHARD_COUNT,
        service_name=MONGOD_SVC_NAME,
    )


@fixture(scope="module")
def vm_mongos_server_certs(issuer: str, namespace: str):
    return create_mongodb_tls_certs(
        ISSUER_CA_NAME,
        namespace,
        MONGOS_STS_NAME,
        MONGOS_CERT_SECRET,
        replicas=MONGOS_COUNT,
        service_name=MONGOS_SVC_NAME,
    )


@fixture(scope="module")
def vm_mongod_tls_pem_secret(namespace: str, vm_mongod_server_certs: str):
    data = read_secret(namespace, vm_mongod_server_certs)
    create_or_update_secret(
        namespace,
        MONGOD_TLS_PEM_SECRET,
        {"server.pem": data["tls.crt"] + data["tls.key"], "ca.pem": data["ca.crt"]},
    )
    return MONGOD_TLS_PEM_SECRET


@fixture(scope="module")
def vm_mongos_tls_pem_secret(namespace: str, vm_mongos_server_certs: str):
    data = read_secret(namespace, vm_mongos_server_certs)
    create_or_update_secret(
        namespace,
        MONGOS_TLS_PEM_SECRET,
        {"server.pem": data["tls.crt"] + data["tls.key"], "ca.pem": data["ca.crt"]},
    )
    return MONGOS_TLS_PEM_SECRET


@fixture(scope="module")
def operator_server_certs(issuer: str, namespace: str):
    return create_mongodb_tls_certs(
        ISSUER_CA_NAME,
        namespace,
        MDB_RESOURCE_NAME,
        OPERATOR_CERT_SECRET,
        replicas=CONFIG_SERVER_COUNT + SHARD_COUNT,
    )


@fixture(scope="module")
def vm_om_ca_configmap(namespace: str):
    create_or_update_configmap(namespace, VM_OM_CA_CONFIGMAP_NAME, {"ca.pem": _get_ca_bundle_content()})
    return VM_OM_CA_CONFIGMAP_NAME


@fixture(scope="module")
def vm_sharded_mongod_sts(namespace: str, om_tester: OMTester, vm_mongod_tls_pem_secret: str, vm_om_ca_configmap: str):
    return deploy_vm_sharded_mongod_statefulset(
        namespace,
        om_tester,
        extra_volumes=[
            {"name": "mongod-certs", "secret": {"secretName": vm_mongod_tls_pem_secret}},
            {"name": "om-ca", "configMap": {"name": vm_om_ca_configmap}},
        ],
        extra_volume_mounts=[
            {
                "name": "mongod-certs",
                "mountPath": "/mongodb-automation/server.pem",
                "subPath": "server.pem",
                "readOnly": True,
            },
            {
                "name": "mongod-certs",
                "mountPath": "/mongodb-automation/tls/ca/ca-pem",
                "subPath": "ca.pem",
                "readOnly": True,
            },
            {"name": "om-ca", "mountPath": "/etc/mongodb-mms-ca", "readOnly": True},
        ],
    )


@fixture(scope="module")
def vm_sharded_mongos_sts(namespace: str, om_tester: OMTester, vm_mongos_tls_pem_secret: str, vm_om_ca_configmap: str):
    return deploy_vm_sharded_mongos_statefulset(
        namespace,
        om_tester,
        extra_volumes=[
            {"name": "mongos-certs", "secret": {"secretName": vm_mongos_tls_pem_secret}},
            {"name": "om-ca", "configMap": {"name": vm_om_ca_configmap}},
        ],
        extra_volume_mounts=[
            {
                "name": "mongos-certs",
                "mountPath": "/mongodb-automation/server.pem",
                "subPath": "server.pem",
                "readOnly": True,
            },
            {
                "name": "mongos-certs",
                "mountPath": "/mongodb-automation/tls/ca/ca-pem",
                "subPath": "ca.pem",
                "readOnly": True,
            },
            {"name": "om-ca", "mountPath": "/etc/mongodb-mms-ca", "readOnly": True},
        ],
    )


@fixture(scope="module")
def vm_sharded_service(namespace: str):
    return deploy_vm_sharded_service(namespace)


@fixture(scope="module")
def vm_sharded_mongos_service(namespace: str):
    return deploy_vm_sharded_mongos_service(namespace)


def _configure_ac(namespace: str, om_tester: OMTester, mdb_version: str) -> None:
    ac_existing = om_tester.api_get_automation_config()
    if len(ac_existing.get("processes", [])) > 0:
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
        tls=True,
    )
    ac["ssl"] = {
        "CAFilePath": "/mongodb-automation/tls/ca/ca-pem",
        "clientCertificateMode": "OPTIONAL",
    }
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
                "roles": [{"role": "readWriteAnyDatabase", "db": "admin"}],
                "mechanisms": ["SCRAM-SHA-256"],
                "scramSha256Creds": build_sha256_creds(APP_USER_PASSWORD),
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
    om_tester.api_put_automation_config(ac)


@fixture(scope="module")
def generated_cr_yaml(namespace: str) -> str:
    create_or_update_secret(namespace, "app-user-secret", {"password": APP_USER_PASSWORD})
    return run_generate_cr(
        namespace,
        resource_name_override=MDB_RESOURCE_NAME,
        user_secrets={"app-user:admin": "app-user-secret"},
        certs_secret_prefix=CERT_SECRET_PREFIX,
    )


@fixture(scope="module")
def generated_cr(generated_cr_yaml: str) -> dict:
    return generated_mongodb_doc(generated_cr_yaml)


@fixture(scope="module")
def migrate_tool_ca_configmap(namespace: str, issuer_ca_filepath: str, generated_cr: dict) -> str:
    ca_name = generated_cr["spec"]["security"]["tls"]["ca"]
    ca_pem = open(issuer_ca_filepath).read()
    create_or_update_configmap(namespace, ca_name, {"ca-pem": ca_pem})
    return ca_name


@fixture(scope="module")
def mdb_migration(
    namespace: str,
    generated_cr_yaml: str,
    operator_server_certs: str,
    migrate_tool_ca_configmap: str,
) -> MongoDB:
    create_wrong_ca_configmap(namespace, WRONG_CA_NAME)

    def swap_to_wrong_ca(resource_doc: dict) -> None:
        resource_doc["spec"]["security"]["tls"]["ca"] = WRONG_CA_NAME

    return apply_generated_sharded_cluster_resource(
        namespace,
        generated_cr_yaml,
        config_rs_name=VM_CONFIG_RS_NAME,
        prepare_external_resources=swap_to_wrong_ca,
    )


@fixture(scope="module")
def scram_opts() -> list[dict]:
    return [with_scram("app-user", APP_USER_PASSWORD, "SCRAM-SHA-256")]


@fixture(scope="module")
def mdb_health_checker(mdb_migration: MongoDB, ca_path: str, scram_opts: list[dict]) -> MongoDBBackgroundTester:
    return MongoDBBackgroundTester(
        mdb_migration.tester(use_ssl=True, ca_path=ca_path),
        health_function_params={"attempts": 1, "opts": scram_opts},
    )


@mark.e2e_vm_migration_shardedcluster_scram_sha256_tls
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


@mark.e2e_vm_migration_shardedcluster_scram_sha256_tls
def test_configure_ac(
    namespace: str,
    om_tester: OMTester,
    vm_sharded_mongod_sts,
    vm_sharded_mongos_sts,
    vm_sharded_service,
    vm_sharded_mongos_service,
    custom_mdb_version: str,
):
    _configure_ac(namespace, om_tester, ensure_ent_version(custom_mdb_version))
    om_tester.wait_agents_ready(timeout=600)


@mark.e2e_vm_migration_shardedcluster_scram_sha256_tls
@skip_if_local()
def test_user_connectivity_before_migration(namespace: str, ca_path: str, scram_opts: list[dict]):
    vm_mongos_tester(MONGOS_STS_NAME, MONGOS_SVC_NAME, namespace).assert_scram_sha_authentication(
        username="app-user",
        password=APP_USER_PASSWORD,
        auth_mechanism="SCRAM-SHA-256",
        ssl=True,
        tlsCAFile=ca_path,
    )


@mark.e2e_vm_migration_shardedcluster_scram_sha256_tls
@skip_if_local()
def test_insert_migration_data(namespace: str, ca_path: str, scram_opts: list[dict]):
    insert_migration_data(
        vm_mongos_tester(MONGOS_STS_NAME, MONGOS_SVC_NAME, namespace, ca_path=ca_path),
        opts=scram_opts,
    )


@mark.e2e_vm_migration_shardedcluster_scram_sha256_tls
@skip_if_local()
def test_non_tls_connection_rejected_before_migration(namespace: str):
    """TLS is enforced on the VM sharded cluster -- plain connections must be rejected."""
    vm_mongos_tester(MONGOS_STS_NAME, MONGOS_SVC_NAME, namespace).assert_no_connection()


@mark.e2e_vm_migration_shardedcluster_scram_sha256_tls
def test_install_operator(operator: Operator):
    operator.assert_is_running()


@mark.e2e_vm_migration_shardedcluster_scram_sha256_tls
def test_common_generated_cr_shape(generated_cr: dict):
    assert_common_generated_sharded_cr_shape(
        generated_cr,
        expected_config_count=CONFIG_SERVER_COUNT,
        expected_shard_count=SHARD_COUNT,
        expected_mongos_count=MONGOS_COUNT,
    )


@mark.e2e_vm_migration_shardedcluster_scram_sha256_tls
def test_tls_enabled_in_cr(generated_cr: dict):
    security = generated_cr.get("spec", {}).get("security", {})
    prefix = security.get("certsSecretPrefix")
    assert prefix, f"Expected certsSecretPrefix to be set for TLS, got security: {security}"


@mark.e2e_vm_migration_shardedcluster_scram_sha256_tls
def test_no_disabled_tls_mode_in_additional_config(generated_cr: dict):
    """additionalMongodConfig must NOT contain net.tls.mode: disabled in any component."""
    for component in ("configSrv", "shard", "mongos"):
        amc = generated_cr["spec"].get(component, {}).get("additionalMongodConfig", {})
        tls_mode = amc.get("net", {}).get("tls", {}).get("mode")
        assert (
            tls_mode != "disabled"
        ), f"TLS is requireSSL -- {component}.additionalMongodConfig should not have mode=disabled, got: {amc}"


@mark.e2e_vm_migration_shardedcluster_scram_sha256_tls
def test_security_auth_present(generated_cr: dict):
    auth = generated_cr["spec"]["security"].get("authentication", {})
    assert auth.get("enabled") is True
    assert "SCRAM" in auth.get("modes", [])


@mark.e2e_vm_migration_shardedcluster_scram_sha256_tls
def test_user_cr_emitted(generated_cr_yaml: str):
    user_docs = generated_user_docs(generated_cr_yaml)
    assert len(user_docs) == 1, f"Expected 1 user CR, got {len(user_docs)}"


@mark.e2e_vm_migration_shardedcluster_scram_sha256_tls
def test_migration_dry_run_wrong_ca_fails_then_passes(
    namespace: str,
    mdb_migration: MongoDB,
    migrate_tool_ca_configmap: str,
):
    run_wrong_ca_dry_run_fails_then_passes(
        namespace,
        mdb_migration,
        f"{MDB_RESOURCE_NAME}-connectivity-check",
        WRONG_CA_NAME,
        correct_ca_name=migrate_tool_ca_configmap,
    )


@mark.e2e_vm_migration_shardedcluster_scram_sha256_tls
def test_migration_dry_run_connectivity_passes(mdb_migration: MongoDB):
    run_migration_dry_run_connectivity_passes(mdb_migration)


@mark.e2e_vm_migration_shardedcluster_scram_sha256_tls
def test_migrate_vm_to_kubernetes(mdb_migration: MongoDB):
    mdb_migration.assert_reaches_phase(Phase.Running, timeout=1800)


@mark.e2e_vm_migration_shardedcluster_scram_sha256_tls
def test_user_crs_reach_updated(generated_cr_yaml: str, namespace: str, mdb_migration: MongoDB, om_tester: OMTester):
    apply_user_crs_and_verify_ac(generated_cr_yaml, namespace, om_tester)


@mark.e2e_vm_migration_shardedcluster_scram_sha256_tls
@skip_if_local()
def test_user_connectivity_after_migration(mdb_migration: MongoDB, ca_path: str):
    mdb_migration.tester(use_ssl=True, ca_path=ca_path).assert_scram_sha_authentication(
        username="app-user",
        password=APP_USER_PASSWORD,
        auth_mechanism="SCRAM-SHA-256",
        ssl=True,
        tlsCAFile=ca_path,
    )


@mark.e2e_vm_migration_shardedcluster_scram_sha256_tls
@skip_if_local()
def test_migration_data_exists_after_migration(mdb_migration: MongoDB, ca_path: str, scram_opts: list[dict]):
    assert_migration_data_exists(mdb_migration.tester(use_ssl=True, ca_path=ca_path), opts=scram_opts)


@mark.e2e_vm_migration_shardedcluster_scram_sha256_tls
@skip_if_local()
def test_start_background_health_checker(mdb_health_checker: MongoDBBackgroundTester):
    mdb_health_checker.start()


@mark.e2e_vm_migration_shardedcluster_scram_sha256_tls
@skip_if_local()
def test_non_tls_connection_rejected_after_migration(mdb_migration: MongoDB):
    """TLS remains enforced after migration -- plain connections must still be rejected."""
    mdb_migration.tester(use_ssl=False).assert_no_connection()


@mark.e2e_vm_migration_shardedcluster_scram_sha256_tls
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


@mark.e2e_vm_migration_shardedcluster_scram_sha256_tls
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


@mark.e2e_vm_migration_shardedcluster_scram_sha256_tls
def test_prune_mongos(mdb_migration: MongoDB):
    try_load(mdb_migration)
    mongos_external = [m for m in mdb_migration["spec"]["externalMembers"] if m["type"] == "mongos"]
    for m in mongos_external:
        mdb_migration["spec"]["externalMembers"].remove(m)
    mdb_migration.update()
    mdb_migration.assert_reaches_phase(Phase.Running)


@mark.e2e_vm_migration_shardedcluster_scram_sha256_tls
@skip_if_local()
def test_mongodb_reachable_during_promote_and_prune(mdb_health_checker: MongoDBBackgroundTester):
    mdb_health_checker.assert_healthiness()
    mdb_health_checker.stop()


@mark.e2e_vm_migration_shardedcluster_scram_sha256_tls
def test_connection_string_after_full_migration(mdb_migration: MongoDB):
    assert_connection_string_after_full_sharded_migration(mdb_migration)


@mark.e2e_vm_migration_shardedcluster_scram_sha256_tls
def test_process_names(om_tester: OMTester, mdb_migration: MongoDB):
    assert_k8s_sharded_process_names(om_tester, mdb_migration)


@mark.e2e_vm_migration_shardedcluster_scram_sha256_tls
@skip_if_local()
def test_user_connectivity_after_promote(mdb_migration: MongoDB, ca_path: str):
    mdb_migration.tester(use_ssl=True, ca_path=ca_path).assert_scram_sha_authentication(
        username="app-user",
        password=APP_USER_PASSWORD,
        auth_mechanism="SCRAM-SHA-256",
        ssl=True,
        tlsCAFile=ca_path,
    )


@mark.e2e_vm_migration_shardedcluster_scram_sha256_tls
@skip_if_local()
def test_migration_data_exists_after_promote(mdb_migration: MongoDB, ca_path: str, scram_opts: list[dict]):
    assert_migration_data_exists(mdb_migration.tester(use_ssl=True, ca_path=ca_path), opts=scram_opts)


@mark.e2e_vm_migration_shardedcluster_scram_sha256_tls
@skip_if_local()
def test_non_tls_connection_rejected_after_promote(mdb_migration: MongoDB):
    """TLS remains enforced after promote and prune."""
    mdb_migration.tester(use_ssl=False).assert_no_connection()


@mark.e2e_vm_migration_shardedcluster_scram_sha256_tls
def test_password_rotation_keeps_migrated_flag(generated_cr_yaml: str, namespace: str, om_tester: OMTester):
    rotate_password_and_verify(generated_cr_yaml, namespace, om_tester)
