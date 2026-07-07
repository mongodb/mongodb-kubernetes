"""
VM migration E2E tests with X509 and TLS for sharded clusters.

The VM sharded cluster (config server + shard + mongos) runs with TLS and X509 client auth;
internal cluster auth between mongod processes continues to use keyFile.

Flow: bring VM agents to goal state with TLS then X509, run kubectl-mongodb migrate-to-mck
to generate the MongoDB CR, apply the generated resource (dry-run, migration, promote/prune).

Cert layout:
- vm-sharded-configsrv-cert: covers the config server pods
  mounted at MONGOD_SERVER_PEM_PATH on the config server StatefulSet
- vm-sharded-shard-cert: covers the shard pods
  mounted at MONGOD_SERVER_PEM_PATH on the shard StatefulSet
- vm-sharded-mongos-cert: covers the mongos pods
  mounted at MONGOS_SERVER_PEM_PATH on the mongos StatefulSet
- agent cert: shared across all VM pods (combined PEM at CUSTOM_AGENT_CERT_PATH)
- CA from ca-key-pair Secret, mounted at CUSTOM_CA_PEM_PATH on all pods
- K8s-side certs created with secret_prefix="mdb-" to match certsSecretPrefix used by migrate-to-mck
"""

from cryptography import x509 as crypto_x509
from cryptography.hazmat.backends import default_backend
from kubetester import create_or_update_configmap, create_or_update_secret, get_statefulset, read_secret, try_load
from kubetester.certs import (
    ISSUER_CA_NAME,
    create_mongodb_tls_certs,
    create_sharded_cluster_certs,
    create_x509_agent_tls_certs,
)
from kubetester.kubetester import KubernetesTester
from kubetester.mongodb import MongoDB
from kubetester.mongotester import MongoDBBackgroundTester, MongoTester, with_x509
from kubetester.omtester import OMContext, OMTester
from kubetester.phase import Phase
from pytest import fixture, mark
from tests.vm_migration.vm_migration_common_helper import (
    assert_max_voting_members_validation,
    generated_mongodb_doc,
    run_generate_cr,
)
from tests.vm_migration.vm_migration_dry_run import run_migration_dry_run_connectivity_passes
from tests.vm_migration.vm_migration_sharded_helper import (
    MIN_K8S_CONFIGSRV,
    MIN_K8S_MONGOS,
    MIN_VM_CONFIGSRV,
    MIN_VM_MONGOS,
    MIN_VM_SHARD,
    apply_generated_sharded_cluster_resource,
    assert_common_generated_sharded_cr_shape,
    assert_k8s_sharded_process_names,
    build_sharded_cluster_ac,
    deploy_vm_sharded_configsrv_service,
    deploy_vm_sharded_configsrv_statefulset,
    deploy_vm_sharded_mongos_service,
    deploy_vm_sharded_mongos_statefulset,
    deploy_vm_sharded_shard_service,
    deploy_vm_sharded_shard_statefulset,
    promote_and_prune_shard,
)

CONFIGSRV_STS_NAME = "vm-sharded-configsrv"
SHARD_STS_NAME = "vm-sharded-shard"
MONGOS_STS_NAME = "vm-sharded-mongos"
CONFIGSRV_SVC_NAME = "vm-sharded-configsrv"
SHARD_SVC_NAME = "vm-sharded-shard"
MONGOS_SVC_NAME = "vm-sharded-mongos"
MDB_RESOURCE_NAME = "sharded-migration"

MONGODB_VERSION = "7.0.14"
CERT_SECRET_PREFIX = "mdb"
VM_CONFIG_RS_NAME = "sharded-migration-config"
VM_SHARD_RS_NAME = "vm-shard-0"

MONGOD_SERVER_PEM_PATH = "/mongodb-automation/server.pem"
MONGOS_SERVER_PEM_PATH = "/mongodb-automation/server.pem"
CUSTOM_CA_PEM_PATH = "/mongodb-automation/tls/ca/ca-pem"

CUSTOM_AGENT_CERT_DIR = "/var/lib/mongodb-mms-automation/certs"
CUSTOM_AGENT_CERT_FILENAME = "agent.pem"
CUSTOM_AGENT_CERT_PATH = f"{CUSTOM_AGENT_CERT_DIR}/{CUSTOM_AGENT_CERT_FILENAME}"


@fixture(scope="module")
def om_tester(namespace: str, operator) -> OMTester:
    config_map = KubernetesTester.read_configmap(namespace, "my-project")
    secret = KubernetesTester.read_secret(namespace, "my-credentials")
    tester = OMTester(OMContext.build_from_config_map_and_secret(config_map, secret))
    tester.ensure_agent_api_key()
    return tester


@fixture(scope="module")
def vm_configsrv_server_certs(issuer: str, namespace: str) -> str:
    return create_mongodb_tls_certs(
        ISSUER_CA_NAME,
        namespace,
        CONFIGSRV_STS_NAME,
        f"{CONFIGSRV_STS_NAME}-cert",
        MIN_VM_CONFIGSRV,
        None,
        CONFIGSRV_SVC_NAME,
    )


@fixture(scope="module")
def vm_shard_server_certs(issuer: str, namespace: str) -> str:
    return create_mongodb_tls_certs(
        ISSUER_CA_NAME,
        namespace,
        SHARD_STS_NAME,
        f"{SHARD_STS_NAME}-cert",
        MIN_VM_SHARD,
        None,
        SHARD_SVC_NAME,
    )


@fixture(scope="module")
def vm_mongos_server_certs(issuer: str, namespace: str) -> str:
    return create_mongodb_tls_certs(
        ISSUER_CA_NAME,
        namespace,
        MONGOS_STS_NAME,
        f"{MONGOS_STS_NAME}-cert",
        MIN_VM_MONGOS,
        None,
        MONGOS_SVC_NAME,
    )


@fixture(scope="module")
def vm_agent_certs(issuer: str, namespace: str) -> str:
    return create_x509_agent_tls_certs(issuer, namespace, MDB_RESOURCE_NAME)


@fixture(scope="module")
def vm_agent_combined_pem(namespace: str, vm_agent_certs: str) -> tuple:
    """Combined PEM secret for VM pod cert mount. Returns (secret_name, subject_dn)."""
    data = read_secret(namespace, vm_agent_certs)
    cert_pem = data.get("tls.crt", b"")
    key_pem = data.get("tls.key", b"")
    if isinstance(cert_pem, bytes):
        cert_pem = cert_pem.decode("utf-8")
    if isinstance(key_pem, bytes):
        key_pem = key_pem.decode("utf-8")

    cert_obj = crypto_x509.load_pem_x509_certificate(cert_pem.encode(), default_backend())
    subject_dn = cert_obj.subject.rfc4514_string()

    pem_secret_name = "vm-sharded-agent-cert-pem"
    create_or_update_secret(namespace, pem_secret_name, {CUSTOM_AGENT_CERT_FILENAME: cert_pem + key_pem})
    return pem_secret_name, subject_dn


@fixture(scope="module")
def x509_client_pem_path(tmp_path_factory, namespace: str, vm_agent_certs: str) -> str:
    data = read_secret(namespace, vm_agent_certs)
    cert_pem = data.get("tls.crt", b"")
    key_pem = data.get("tls.key", b"")
    if isinstance(cert_pem, bytes):
        cert_pem = cert_pem.decode("utf-8")
    if isinstance(key_pem, bytes):
        key_pem = key_pem.decode("utf-8")
    pem_path = tmp_path_factory.mktemp("x509-client") / "agent.pem"
    pem_path.write_text(cert_pem + key_pem)
    return str(pem_path)


@fixture(scope="module")
def x509_opts(x509_client_pem_path: str, issuer_ca_filepath: str) -> list[dict]:
    return [with_x509(x509_client_pem_path, issuer_ca_filepath)]


@fixture(scope="module")
def vm_configsrv_server_combined_pem(namespace: str, vm_configsrv_server_certs: str) -> str:
    """Combined cert+key PEM for the config server StatefulSet (certificateKeyFile requires one file)."""
    data = read_secret(namespace, vm_configsrv_server_certs)
    cert_pem = data.get("tls.crt", b"")
    key_pem = data.get("tls.key", b"")
    if isinstance(cert_pem, bytes):
        cert_pem = cert_pem.decode("utf-8")
    if isinstance(key_pem, bytes):
        key_pem = key_pem.decode("utf-8")
    secret_name = "vm-sharded-configsrv-server-pem"
    create_or_update_secret(namespace, secret_name, {"server.pem": cert_pem + key_pem})
    return secret_name


@fixture(scope="module")
def vm_shard_server_combined_pem(namespace: str, vm_shard_server_certs: str) -> str:
    """Combined cert+key PEM for the shard StatefulSet."""
    data = read_secret(namespace, vm_shard_server_certs)
    cert_pem = data.get("tls.crt", b"")
    key_pem = data.get("tls.key", b"")
    if isinstance(cert_pem, bytes):
        cert_pem = cert_pem.decode("utf-8")
    if isinstance(key_pem, bytes):
        key_pem = key_pem.decode("utf-8")
    secret_name = "vm-sharded-shard-server-pem"
    create_or_update_secret(namespace, secret_name, {"server.pem": cert_pem + key_pem})
    return secret_name


@fixture(scope="module")
def vm_mongos_server_combined_pem(namespace: str, vm_mongos_server_certs: str) -> str:
    """Combined cert+key PEM for the mongos StatefulSet."""
    data = read_secret(namespace, vm_mongos_server_certs)
    cert_pem = data.get("tls.crt", b"")
    key_pem = data.get("tls.key", b"")
    if isinstance(cert_pem, bytes):
        cert_pem = cert_pem.decode("utf-8")
    if isinstance(key_pem, bytes):
        key_pem = key_pem.decode("utf-8")
    secret_name = "vm-sharded-mongos-server-pem"
    create_or_update_secret(namespace, secret_name, {"server.pem": cert_pem + key_pem})
    return secret_name


@fixture(scope="module")
def mdb_sharded_x509_certs(issuer: str, namespace: str) -> None:
    """K8s-side TLS cert secrets for the MongoDB sharded resource.

    secret_prefix="mdb-" ensures the generated names match what migrate-to-mck
    produces when called with --certs-secret-prefix mdb.
    """
    create_sharded_cluster_certs(
        namespace=namespace,
        resource_name=MDB_RESOURCE_NAME,
        shards=1,
        mongod_per_shard=MIN_VM_SHARD,
        config_servers=MIN_K8S_CONFIGSRV,
        mongos=MIN_K8S_MONGOS,
        x509_certs=True,
        secret_prefix=f"{CERT_SECRET_PREFIX}-",
    )


def _server_extra_volumes(server_combined_pem: str, agent_secret_name: str) -> list[dict]:
    return [
        {
            "name": "mongodb-certs",
            "secret": {
                "secretName": server_combined_pem,
                "items": [{"key": "server.pem", "path": "server.pem"}],
            },
        },
        {
            "name": "ca-cert",
            "secret": {
                "secretName": "ca-key-pair",
                "items": [{"key": "tls.crt", "path": "ca-pem"}],
            },
        },
        {"name": "agent-cert", "secret": {"secretName": agent_secret_name}},
    ]


def _server_extra_volume_mounts() -> list[dict]:
    return [
        {"name": "mongodb-certs", "mountPath": "/mongodb-automation", "readOnly": True},
        {"name": "ca-cert", "mountPath": "/mongodb-automation/tls/ca", "readOnly": True},
        {"name": "agent-cert", "mountPath": CUSTOM_AGENT_CERT_DIR, "readOnly": True},
    ]


@fixture(scope="module")
def vm_sharded_configsrv_sts(
    namespace: str,
    om_tester: OMTester,
    vm_configsrv_server_combined_pem: str,
    vm_agent_combined_pem: tuple,
):
    agent_secret_name, _ = vm_agent_combined_pem
    return deploy_vm_sharded_configsrv_statefulset(
        namespace,
        om_tester,
        extra_volumes=_server_extra_volumes(vm_configsrv_server_combined_pem, agent_secret_name),
        extra_volume_mounts=_server_extra_volume_mounts(),
    )


@fixture(scope="module")
def vm_sharded_shard_sts(
    namespace: str,
    om_tester: OMTester,
    vm_shard_server_combined_pem: str,
    vm_agent_combined_pem: tuple,
):
    agent_secret_name, _ = vm_agent_combined_pem
    return deploy_vm_sharded_shard_statefulset(
        namespace,
        om_tester,
        extra_volumes=_server_extra_volumes(vm_shard_server_combined_pem, agent_secret_name),
        extra_volume_mounts=_server_extra_volume_mounts(),
    )


@fixture(scope="module")
def vm_sharded_mongos_sts(
    namespace: str,
    om_tester: OMTester,
    vm_mongos_server_combined_pem: str,
    vm_agent_combined_pem: tuple,
):
    agent_secret_name, _ = vm_agent_combined_pem
    return deploy_vm_sharded_mongos_statefulset(
        namespace,
        om_tester,
        extra_volumes=_server_extra_volumes(vm_mongos_server_combined_pem, agent_secret_name),
        extra_volume_mounts=_server_extra_volume_mounts(),
    )


@fixture(scope="module")
def vm_sharded_configsrv_service(namespace: str):
    return deploy_vm_sharded_configsrv_service(namespace)


@fixture(scope="module")
def vm_sharded_shard_service(namespace: str):
    return deploy_vm_sharded_shard_service(namespace)


@fixture(scope="module")
def vm_sharded_mongos_service(namespace: str):
    return deploy_vm_sharded_mongos_service(namespace)


@fixture(scope="module")
def generated_cr_yaml(namespace: str) -> str:
    return run_generate_cr(
        namespace,
        certs_secret_prefix=CERT_SECRET_PREFIX,
        resource_name_override=MDB_RESOURCE_NAME,
    )


@fixture(scope="module")
def generated_cr(generated_cr_yaml: str) -> dict:
    return generated_mongodb_doc(generated_cr_yaml)


@fixture(scope="module")
def migrate_tool_ca_configmap(namespace: str, issuer_ca_filepath: str, generated_cr: dict) -> str:
    ca_name = generated_cr["spec"]["security"]["tls"]["ca"]
    create_or_update_configmap(namespace, ca_name, {"ca-pem": open(issuer_ca_filepath).read()})
    return ca_name


@fixture(scope="module")
def mdb_migration(
    namespace: str,
    generated_cr_yaml: str,
    mdb_sharded_x509_certs,
    migrate_tool_ca_configmap: str,
    vm_agent_certs: str,
) -> MongoDB:
    def create_agent_cert_secret(resource_doc: dict) -> None:
        certs_prefix = resource_doc["spec"]["security"]["certsSecretPrefix"]
        resource_name = resource_doc["metadata"]["name"]
        agent_cert_secret_name = f"{certs_prefix}-{resource_name}-agent-certs"
        agent_cert = read_secret(namespace, vm_agent_certs)
        create_or_update_secret(
            namespace,
            agent_cert_secret_name,
            {"tls.crt": agent_cert["tls.crt"], "tls.key": agent_cert["tls.key"]},
            type="kubernetes.io/tls",
        )

    return apply_generated_sharded_cluster_resource(
        namespace,
        generated_cr_yaml,
        config_rs_name=VM_CONFIG_RS_NAME,
        prepare_external_resources=create_agent_cert_secret,
    )


@fixture(scope="module")
def mongo_tester(mdb_migration: MongoDB, issuer_ca_filepath: str) -> MongoTester:
    return mdb_migration.tester(use_ssl=True, ca_path=issuer_ca_filepath)


@fixture(scope="module")
def mdb_health_checker(mongo_tester: MongoTester, x509_opts: list[dict]) -> MongoDBBackgroundTester:
    return MongoDBBackgroundTester(
        mongo_tester,
        allowed_sequential_failures=15,
        health_function_params={"attempts": 1, "opts": x509_opts},
    )


@mark.e2e_vm_migration_shardedcluster_x509
def test_deploy_vm_sharded(
    namespace: str,
    vm_sharded_configsrv_sts,
    vm_sharded_shard_sts,
    vm_sharded_mongos_sts,
    vm_sharded_configsrv_service,
    vm_sharded_shard_service,
    vm_sharded_mongos_service,
):
    def configsrv_ready():
        sts = get_statefulset(namespace, vm_sharded_configsrv_sts["metadata"]["name"])
        return sts.status.ready_replicas == vm_sharded_configsrv_sts["spec"]["replicas"]

    def shard_ready():
        sts = get_statefulset(namespace, vm_sharded_shard_sts["metadata"]["name"])
        return sts.status.ready_replicas == vm_sharded_shard_sts["spec"]["replicas"]

    def mongos_ready():
        sts = get_statefulset(namespace, vm_sharded_mongos_sts["metadata"]["name"])
        return sts.status.ready_replicas == vm_sharded_mongos_sts["spec"]["replicas"]

    KubernetesTester.wait_until(configsrv_ready, timeout=300)
    KubernetesTester.wait_until(shard_ready, timeout=300)
    KubernetesTester.wait_until(mongos_ready, timeout=300)


@mark.e2e_vm_migration_shardedcluster_x509
def test_vm_sharded_ac_no_auth(
    namespace: str,
    om_tester: OMTester,
    vm_sharded_configsrv_sts,
    vm_sharded_shard_sts,
    vm_sharded_configsrv_service,
    vm_sharded_shard_service,
    vm_sharded_mongos_sts,
    vm_sharded_mongos_service,
):
    """Put the initial AC with no auth so agents are able to reach goal state."""
    if len(om_tester.api_get_automation_config().get("processes", [])) > 0:
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
        mongodb_version=MONGODB_VERSION,
        config_rs_name=VM_CONFIG_RS_NAME,
        shard_rs_name=VM_SHARD_RS_NAME,
        config_server_count=MIN_VM_CONFIGSRV,
        shard_count=MIN_VM_SHARD,
        mongos_count=MIN_VM_MONGOS,
    )
    om_tester.api_put_automation_config(ac)
    om_tester.wait_agents_ready(timeout=1800)


@mark.e2e_vm_migration_shardedcluster_x509
def test_vm_sharded_ac_tls(
    namespace: str, om_tester: OMTester, vm_sharded_configsrv_sts, vm_sharded_shard_sts, vm_sharded_mongos_sts
):
    """Enable TLS on all VM sharded cluster processes."""
    ac = build_sharded_cluster_ac(
        om_tester,
        configsrv_sts_name=CONFIGSRV_STS_NAME,
        shard_sts_name=SHARD_STS_NAME,
        mongos_sts_name=MONGOS_STS_NAME,
        configsrv_service_name=CONFIGSRV_SVC_NAME,
        shard_service_name=SHARD_SVC_NAME,
        mongos_service_name=MONGOS_SVC_NAME,
        namespace=namespace,
        mongodb_version=MONGODB_VERSION,
        config_rs_name=VM_CONFIG_RS_NAME,
        shard_rs_name=VM_SHARD_RS_NAME,
        config_server_count=MIN_VM_CONFIGSRV,
        shard_count=MIN_VM_SHARD,
        mongos_count=MIN_VM_MONGOS,
        tls=True,
        mongod_cert_path=MONGOD_SERVER_PEM_PATH,
        mongos_cert_path=MONGOS_SERVER_PEM_PATH,
        ca_cert_path=CUSTOM_CA_PEM_PATH,
        agent_cert_path=CUSTOM_AGENT_CERT_PATH,
    )
    om_tester.api_put_automation_config(ac)
    om_tester.wait_agents_ready(timeout=1800)


@mark.e2e_vm_migration_shardedcluster_x509
def test_vm_sharded_ac_x509(namespace: str, om_tester: OMTester, vm_agent_combined_pem: tuple):
    """Switch the VM sharded cluster AC to X509 client auth."""
    _, agent_subject_dn = vm_agent_combined_pem
    ac = build_sharded_cluster_ac(
        om_tester,
        configsrv_sts_name=CONFIGSRV_STS_NAME,
        shard_sts_name=SHARD_STS_NAME,
        mongos_sts_name=MONGOS_STS_NAME,
        configsrv_service_name=CONFIGSRV_SVC_NAME,
        shard_service_name=SHARD_SVC_NAME,
        mongos_service_name=MONGOS_SVC_NAME,
        namespace=namespace,
        mongodb_version=MONGODB_VERSION,
        config_rs_name=VM_CONFIG_RS_NAME,
        shard_rs_name=VM_SHARD_RS_NAME,
        config_server_count=MIN_VM_CONFIGSRV,
        shard_count=MIN_VM_SHARD,
        mongos_count=MIN_VM_MONGOS,
        tls=True,
        mongod_cert_path=MONGOD_SERVER_PEM_PATH,
        mongos_cert_path=MONGOS_SERVER_PEM_PATH,
        ca_cert_path=CUSTOM_CA_PEM_PATH,
        agent_cert_path=CUSTOM_AGENT_CERT_PATH,
        x509_agent_subject_dn=agent_subject_dn,
    )
    om_tester.api_put_automation_config(ac)
    om_tester.wait_agents_ready(timeout=600)


@mark.e2e_vm_migration_shardedcluster_x509
def test_vm_sharded_deployment_is_ready(om_tester: OMTester):
    ac_tester = om_tester.get_automation_config_tester()
    vm_total = MIN_VM_CONFIGSRV + MIN_VM_SHARD + MIN_VM_MONGOS
    if len(ac_tester.get_all_processes()) > vm_total:
        return

    om_tester.wait_agents_ready(timeout=1800)
    ac_tester = om_tester.get_automation_config_tester()
    assert len(ac_tester.get_all_processes()) == vm_total
    assert len(ac_tester.get_monitoring_versions()) == vm_total
    assert len(ac_tester.get_backup_versions()) == vm_total
    assert len(ac_tester.get_sharding_entries()) == 1


@mark.e2e_vm_migration_shardedcluster_x509
def test_install_operator(default_operator):
    default_operator.assert_is_running()


@mark.e2e_vm_migration_shardedcluster_x509
def test_common_generated_cr_shape(generated_cr: dict):
    assert_common_generated_sharded_cr_shape(
        generated_cr,
        expected_config_count=MIN_VM_CONFIGSRV,
        expected_shard_count=MIN_VM_SHARD,
        expected_mongos_count=MIN_VM_MONGOS,
    )


@mark.e2e_vm_migration_shardedcluster_x509
def test_x509_agent_auth_in_cr(generated_cr: dict):
    agents = generated_cr["spec"]["security"]["authentication"]["agents"]
    assert agents["mode"] == "X509"
    assert agents["autoPEMKeyFilePath"] == CUSTOM_AGENT_CERT_PATH
    assert agents["clientCertificateSecretRef"]["name"] == f"{CERT_SECRET_PREFIX}-{MDB_RESOURCE_NAME}-agent-certs"


@mark.e2e_vm_migration_shardedcluster_x509
def test_migration_dry_run_connectivity_passes(mdb_migration: MongoDB):
    run_migration_dry_run_connectivity_passes(mdb_migration)


@mark.e2e_vm_migration_shardedcluster_x509
def test_migrate_vm_to_kubernetes(mdb_migration: MongoDB):
    mdb_migration.assert_reaches_phase(Phase.Running, timeout=1800)


@mark.e2e_vm_migration_shardedcluster_x509
def test_max_voting_members_validation(mdb_migration: MongoDB):
    assert_max_voting_members_validation(mdb_migration)


@mark.e2e_vm_migration_shardedcluster_x509
def test_start_background_health_checker(mdb_health_checker: MongoDBBackgroundTester):
    mdb_health_checker.start()


@mark.e2e_vm_migration_shardedcluster_x509
def test_promote_and_prune_config_server(mdb_migration: MongoDB, om_tester: OMTester):
    try_load(mdb_migration)
    config_rs_name = f"{mdb_migration.name}-config"
    for i in range(MIN_VM_CONFIGSRV):
        mdb_migration["spec"]["memberConfig"][i]["priority"] = "1"
        mdb_migration["spec"]["memberConfig"][i]["votes"] = 1
        mdb_migration.update()
        mdb_migration.assert_reaches_phase(Phase.Running)

        config_external = [
            m for m in mdb_migration["spec"]["externalMembers"] if m.get("replicaSetName") == config_rs_name
        ]
        if config_external:
            mdb_migration["spec"]["externalMembers"].remove(config_external[-1])
            mdb_migration.update()
            mdb_migration.assert_reaches_phase(Phase.Running)

        om_tester.assert_cluster_available(mdb_migration.name)


@mark.e2e_vm_migration_shardedcluster_x509
def test_prune_shard(mdb_migration: MongoDB, om_tester: OMTester):
    promote_and_prune_shard(mdb_migration, om_tester, VM_SHARD_RS_NAME, mdb_migration.name)


@mark.e2e_vm_migration_shardedcluster_x509
def test_prune_mongos(mdb_migration: MongoDB, mdb_health_checker: MongoDBBackgroundTester):
    try_load(mdb_migration)

    mongos_external = [m for m in mdb_migration["spec"]["externalMembers"] if m["type"] == "mongos"]
    for m in mongos_external:
        mdb_migration["spec"]["externalMembers"].remove(m)
    mdb_migration.update()
    mdb_migration.assert_reaches_phase(Phase.Running)

    mdb_health_checker.assert_healthiness()


@mark.e2e_vm_migration_shardedcluster_x509
def test_mongodb_reachable_during_promote_and_prune(mdb_health_checker: MongoDBBackgroundTester):
    mdb_health_checker.assert_healthiness()
    mdb_health_checker.stop()


@mark.e2e_vm_migration_shardedcluster_x509
def test_process_names(namespace: str, om_tester: OMTester, mdb_migration: MongoDB):
    assert_k8s_sharded_process_names(om_tester, mdb_migration)


@mark.e2e_vm_migration_shardedcluster_x509
def test_migration_data_exists_after_promote(mdb_migration: MongoDB, issuer_ca_filepath: str, x509_opts: list[dict]):
    mdb_migration.tester(use_ssl=True, ca_path=issuer_ca_filepath).assert_connectivity(opts=x509_opts)
