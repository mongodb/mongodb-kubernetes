"""
VM migration E2E tests with X509 and TLS for sharded clusters.

The VM sharded cluster (config server + shard + mongos) runs with TLS and X509 client auth;
internal cluster auth between mongod processes continues to use keyFile.

Flow: bring VM agents to goal state with TLS then X509, then migrate (MDB sharded resource
with externalMembers pointing at VM processes, run connectivity dry-run, promote K8s members
and prune VM members).

Cert layout:
- vm-sharded-mongod-cert: covers all 6 mongod pods (config-server 0-2, shard 0-2)
  mounted at MONGOD_SERVER_PEM_PATH on the mongod StatefulSet
- vm-sharded-mongos-cert: covers 2 mongos pods
  mounted at MONGOS_SERVER_PEM_PATH on the mongos StatefulSet
- agent cert: shared across all VM pods (combined PEM at CUSTOM_AGENT_CERT_PATH)
- CA from ca-key-pair Secret, mounted at CUSTOM_CA_PEM_PATH on all pods

Why the same server-PEM path on different STSes?
Each StatefulSet mounts its own cert Secret at the same container path. The AC
certificateKeyFile entry points to that path; mongos and mongod never share a pod.
"""

import yaml
from cryptography import x509 as crypto_x509
from cryptography.hazmat.backends import default_backend
from kubetester import create_or_update_secret, get_statefulset, read_secret, try_load
from kubetester.certs import (
    ISSUER_CA_NAME,
    create_mongodb_tls_certs,
    create_sharded_cluster_certs,
    create_x509_agent_tls_certs,
)
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB
from kubetester.mongotester import MongoDBBackgroundTester, MongoTester
from kubetester.omtester import OMContext, OMTester
from kubetester.phase import Phase
from pytest import fixture, mark
from tests.tls.vm_migration_sharded_ac import build_sharded_cluster_ac
from tests.vm_migration.vm_migration_dry_run import run_migration_dry_run_connectivity_passes

MONGOD_STS_NAME = "vm-sharded-mongod"
MONGOS_STS_NAME = "vm-sharded-mongos"
MONGOD_SVC_NAME = "vm-sharded-mongod"
MONGOS_SVC_NAME = "vm-sharded-mongos"
MDB_RESOURCE_NAME = "sharded-migration"

CONFIG_SERVER_COUNT = 3
SHARD_COUNT = 3
MONGOS_COUNT = 2

MONGODB_VERSION = "7.0.14"

# Cert paths inside the container, must match the AC certificateKeyFile values.
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
def vm_mongod_server_certs(issuer: str, namespace: str) -> str:
    """TLS certs for all VM mongod pods (config server 0-2 and shard 0-2)."""
    return create_mongodb_tls_certs(
        ISSUER_CA_NAME,
        namespace,
        MONGOD_STS_NAME,
        f"{MONGOD_STS_NAME}-cert",
        CONFIG_SERVER_COUNT + SHARD_COUNT,
        None,
        MONGOD_SVC_NAME,
    )


@fixture(scope="module")
def vm_mongos_server_certs(issuer: str, namespace: str) -> str:
    """TLS certs for all VM mongos pods."""
    return create_mongodb_tls_certs(
        ISSUER_CA_NAME,
        namespace,
        MONGOS_STS_NAME,
        f"{MONGOS_STS_NAME}-cert",
        MONGOS_COUNT,
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
def vm_mongod_server_combined_pem(namespace: str, vm_mongod_server_certs: str) -> str:
    """Combined cert+key PEM for the mongod StatefulSet (certificateKeyFile requires one file)."""
    data = read_secret(namespace, vm_mongod_server_certs)
    cert_pem = data.get("tls.crt", b"")
    key_pem = data.get("tls.key", b"")
    if isinstance(cert_pem, bytes):
        cert_pem = cert_pem.decode("utf-8")
    if isinstance(key_pem, bytes):
        key_pem = key_pem.decode("utf-8")
    secret_name = "vm-sharded-mongod-server-pem"
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


def _add_tls_volumes_and_mounts(sts_body: dict, server_pem_secret: str, agent_pem_secret: str) -> None:
    """Add server cert, CA, and agent cert volumes and mounts to the STS template."""
    volumes = sts_body["spec"]["template"]["spec"].get("volumes") or []
    volumes.append(
        {
            "name": "mongodb-certs",
            "secret": {
                "secretName": server_pem_secret,
                "items": [{"key": "server.pem", "path": "server.pem"}],
            },
        }
    )
    volumes.append(
        {
            "name": "ca-cert",
            "secret": {
                "secretName": "ca-key-pair",
                "items": [{"key": "tls.crt", "path": "ca-pem"}],
            },
        }
    )
    volumes.append({"name": "agent-cert", "secret": {"secretName": agent_pem_secret}})
    sts_body["spec"]["template"]["spec"]["volumes"] = volumes

    mounts = sts_body["spec"]["template"]["spec"]["containers"][0].get("volumeMounts") or []
    mounts.append({"name": "mongodb-certs", "mountPath": "/mongodb-automation", "readOnly": True})
    mounts.append({"name": "ca-cert", "mountPath": "/mongodb-automation/tls/ca", "readOnly": True})
    mounts.append({"name": "agent-cert", "mountPath": CUSTOM_AGENT_CERT_DIR, "readOnly": True})
    sts_body["spec"]["template"]["spec"]["containers"][0]["volumeMounts"] = mounts


@fixture(scope="module")
def vm_sharded_mongod_sts(
    namespace: str,
    om_tester: OMTester,
    vm_mongod_server_combined_pem: str,
    vm_agent_combined_pem: tuple,
):
    with open(yaml_fixture("vm_sharded_statefulset.yaml"), "r") as f:
        sts_body = yaml.safe_load(f.read())

    sts_body["spec"]["template"]["spec"]["containers"][0]["env"] = [
        {"name": "MMS_GROUP_ID", "value": om_tester.context.project_id},
        {"name": "MMS_BASE_URL", "value": om_tester.context.base_url},
        {"name": "MMS_API_KEY", "value": om_tester.context.agent_api_key},
    ]

    agent_secret_name, _ = vm_agent_combined_pem
    _add_tls_volumes_and_mounts(sts_body, vm_mongod_server_combined_pem, agent_secret_name)

    KubernetesTester.create_or_update_statefulset(namespace, body=sts_body)
    return sts_body


@fixture(scope="module")
def vm_sharded_mongos_sts(
    namespace: str,
    om_tester: OMTester,
    vm_mongos_server_combined_pem: str,
    vm_agent_combined_pem: tuple,
):
    with open(yaml_fixture("vm_sharded_mongos_statefulset.yaml"), "r") as f:
        sts_body = yaml.safe_load(f.read())

    sts_body["spec"]["template"]["spec"]["containers"][0]["env"] = [
        {"name": "MMS_GROUP_ID", "value": om_tester.context.project_id},
        {"name": "MMS_BASE_URL", "value": om_tester.context.base_url},
        {"name": "MMS_API_KEY", "value": om_tester.context.agent_api_key},
    ]

    agent_secret_name, _ = vm_agent_combined_pem
    _add_tls_volumes_and_mounts(sts_body, vm_mongos_server_combined_pem, agent_secret_name)

    KubernetesTester.create_or_update_statefulset(namespace, body=sts_body)
    return sts_body


@fixture(scope="module")
def vm_sharded_service(namespace: str):
    with open(yaml_fixture("vm_sharded_service.yaml"), "r") as f:
        service_body = yaml.safe_load(f.read())
    service_body["spec"]["clusterIP"] = "None"
    KubernetesTester.create_or_update_service(namespace, body=service_body)
    return service_body


@fixture(scope="module")
def vm_sharded_mongos_service(namespace: str):
    with open(yaml_fixture("vm_sharded_mongos_service.yaml"), "r") as f:
        service_body = yaml.safe_load(f.read())
    service_body["spec"]["clusterIP"] = "None"
    KubernetesTester.create_or_update_service(namespace, body=service_body)
    return service_body


@fixture(scope="module")
def mdb_sharded_x509_certs(issuer: str, namespace: str) -> None:
    """TLS server certs for the K8s MongoDB sharded cluster resource.

    Creates three component-specific secrets:
    - {MDB_RESOURCE_NAME}-0-cert      (shard replica set)
    - {MDB_RESOURCE_NAME}-config-cert (config server replica set)
    - {MDB_RESOURCE_NAME}-mongos-cert (mongos)
    """
    create_sharded_cluster_certs(
        namespace=namespace,
        resource_name=MDB_RESOURCE_NAME,
        shards=1,
        mongod_per_shard=SHARD_COUNT,
        config_servers=CONFIG_SERVER_COUNT,
        mongos=MONGOS_COUNT,
        x509_certs=True,
    )


@fixture(scope="module")
def mdb_sharded_migration(
    namespace: str,
    issuer_ca_configmap: str,
    mdb_sharded_x509_certs,
    vm_sharded_mongod_sts,
    vm_sharded_mongos_sts,
    vm_sharded_service,
    vm_sharded_mongos_service,
    vm_agent_certs: str,
) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("vm-migration-sharded.yaml"),
        namespace=namespace,
        with_mdb_version_from_env=False,
    )

    if try_load(resource):
        return resource

    resource.set_version(MONGODB_VERSION)

    config_rs_name = f"{resource.name}-config"
    vm_shard_rs_name = "vm-shard-0"

    resource["spec"]["shardNameOverrides"] = [
        {"shardName": f"{resource.name}-0", "shardId": vm_shard_rs_name, "replicaSetName": vm_shard_rs_name}
    ]

    resource["spec"]["security"] = {
        "tls": {"enabled": True, "ca": issuer_ca_configmap},
        "authentication": {
            "enabled": True,
            "modes": ["X509"],
            "agents": {
                "mode": "X509",
                "clientCertificateSecretRef": {"name": vm_agent_certs},
                "autoPEMKeyFilePath": CUSTOM_AGENT_CERT_PATH,
            },
        },
    }

    resource["spec"]["externalMembers"] = []
    for i in range(CONFIG_SERVER_COUNT):
        resource["spec"]["externalMembers"].append(
            {
                "processName": f"{MONGOD_STS_NAME}-{i}",
                "hostname": f"{MONGOD_STS_NAME}-{i}.{MONGOD_SVC_NAME}.{namespace}.svc.cluster.local:27017",
                "type": "mongod",
                "replicaSetName": config_rs_name,
            }
        )
    for i in range(CONFIG_SERVER_COUNT, CONFIG_SERVER_COUNT + SHARD_COUNT):
        resource["spec"]["externalMembers"].append(
            {
                "processName": f"{MONGOD_STS_NAME}-{i}",
                "hostname": f"{MONGOD_STS_NAME}-{i}.{MONGOD_SVC_NAME}.{namespace}.svc.cluster.local:27017",
                "type": "mongod",
                "replicaSetName": vm_shard_rs_name,
            }
        )
    for i in range(MONGOS_COUNT):
        resource["spec"]["externalMembers"].append(
            {
                "processName": f"{MONGOS_STS_NAME}-{i}",
                "hostname": f"{MONGOS_STS_NAME}-{i}.{MONGOS_SVC_NAME}.{namespace}.svc.cluster.local:27017",
                "type": "mongos",
                "replicaSetName": "",
            }
        )

    resource["spec"]["memberConfig"] = [{"votes": 0, "priority": "0"} for _ in range(CONFIG_SERVER_COUNT)]

    resource.create()
    return resource


@fixture(scope="module")
def mongo_tester(mdb_sharded_migration: MongoDB) -> MongoTester:
    return mdb_sharded_migration.tester()


@fixture(scope="module")
def mdb_health_checker(mongo_tester: MongoTester) -> MongoDBBackgroundTester:
    health_checker = MongoDBBackgroundTester(mongo_tester, allowed_sequential_failures=3)
    health_checker.start()
    return health_checker


@mark.e2e_vm_migration_sharded_x509
def test_deploy_vm_sharded(
    namespace: str,
    vm_sharded_mongod_sts,
    vm_sharded_mongos_sts,
    vm_sharded_service,
    vm_sharded_mongos_service,
):
    def mongod_ready():
        sts = get_statefulset(namespace, vm_sharded_mongod_sts["metadata"]["name"])
        return sts.status.ready_replicas == vm_sharded_mongod_sts["spec"]["replicas"]

    def mongos_ready():
        sts = get_statefulset(namespace, vm_sharded_mongos_sts["metadata"]["name"])
        return sts.status.ready_replicas == vm_sharded_mongos_sts["spec"]["replicas"]

    KubernetesTester.wait_until(mongod_ready, timeout=300)
    KubernetesTester.wait_until(mongos_ready, timeout=300)


@mark.e2e_vm_migration_sharded_x509
def test_vm_sharded_ac_no_auth(
    namespace: str,
    om_tester: OMTester,
    vm_sharded_mongod_sts,
    vm_sharded_service,
    vm_sharded_mongos_sts,
    vm_sharded_mongos_service,
):
    """Put the initial AC with no auth so agents are able to reach goal state."""
    if len(om_tester.api_get_automation_config().get("processes", [])) > 0:
        return
    config_rs_name = "sharded-migration-config"
    vm_shard_rs_name = "vm-shard-0"
    ac = build_sharded_cluster_ac(
        om_tester,
        mongod_sts_name=MONGOD_STS_NAME,
        mongos_sts_name=MONGOS_STS_NAME,
        service_name=MONGOD_SVC_NAME,
        mongos_service_name=MONGOS_SVC_NAME,
        namespace=namespace,
        mongodb_version=MONGODB_VERSION,
        config_rs_name=config_rs_name,
        shard_rs_name=vm_shard_rs_name,
        config_server_count=CONFIG_SERVER_COUNT,
        shard_count=SHARD_COUNT,
        mongos_count=MONGOS_COUNT,
    )
    om_tester.api_put_automation_config(ac)
    om_tester.wait_agents_ready(timeout=1800)


@mark.e2e_vm_migration_sharded_x509
def test_vm_sharded_ac_tls(namespace: str, om_tester: OMTester, vm_sharded_mongod_sts, vm_sharded_mongos_sts):
    """Enable TLS on all VM sharded cluster processes."""
    config_rs_name = "sharded-migration-config"
    vm_shard_rs_name = "vm-shard-0"
    ac = build_sharded_cluster_ac(
        om_tester,
        mongod_sts_name=MONGOD_STS_NAME,
        mongos_sts_name=MONGOS_STS_NAME,
        service_name=MONGOD_SVC_NAME,
        mongos_service_name=MONGOS_SVC_NAME,
        namespace=namespace,
        mongodb_version=MONGODB_VERSION,
        config_rs_name=config_rs_name,
        shard_rs_name=vm_shard_rs_name,
        config_server_count=CONFIG_SERVER_COUNT,
        shard_count=SHARD_COUNT,
        mongos_count=MONGOS_COUNT,
        tls=True,
        mongod_cert_path=MONGOD_SERVER_PEM_PATH,
        mongos_cert_path=MONGOS_SERVER_PEM_PATH,
        ca_cert_path=CUSTOM_CA_PEM_PATH,
        agent_cert_path=CUSTOM_AGENT_CERT_PATH,
    )
    om_tester.api_put_automation_config(ac)
    om_tester.wait_agents_ready(timeout=1800)


@mark.e2e_vm_migration_sharded_x509
def test_vm_sharded_ac_x509(namespace: str, om_tester: OMTester, vm_agent_combined_pem: tuple):
    """Switch the VM sharded cluster AC to X509 client auth."""
    config_rs_name = "sharded-migration-config"
    vm_shard_rs_name = "vm-shard-0"
    _, agent_subject_dn = vm_agent_combined_pem
    ac = build_sharded_cluster_ac(
        om_tester,
        mongod_sts_name=MONGOD_STS_NAME,
        mongos_sts_name=MONGOS_STS_NAME,
        service_name=MONGOD_SVC_NAME,
        mongos_service_name=MONGOS_SVC_NAME,
        namespace=namespace,
        mongodb_version=MONGODB_VERSION,
        config_rs_name=config_rs_name,
        shard_rs_name=vm_shard_rs_name,
        config_server_count=CONFIG_SERVER_COUNT,
        shard_count=SHARD_COUNT,
        mongos_count=MONGOS_COUNT,
        tls=True,
        mongod_cert_path=MONGOD_SERVER_PEM_PATH,
        mongos_cert_path=MONGOS_SERVER_PEM_PATH,
        ca_cert_path=CUSTOM_CA_PEM_PATH,
        agent_cert_path=CUSTOM_AGENT_CERT_PATH,
        x509_agent_subject_dn=agent_subject_dn,
    )
    om_tester.api_put_automation_config(ac)
    om_tester.wait_agents_ready(timeout=600)


@mark.e2e_vm_migration_sharded_x509
def test_vm_sharded_deployment_is_ready(om_tester: OMTester):
    ac_tester = om_tester.get_automation_config_tester()
    vm_total = CONFIG_SERVER_COUNT + SHARD_COUNT + MONGOS_COUNT
    if len(ac_tester.get_all_processes()) > vm_total:
        return

    om_tester.wait_agents_ready(timeout=1800)
    ac_tester = om_tester.get_automation_config_tester()
    assert len(ac_tester.get_all_processes()) == vm_total
    assert len(ac_tester.get_monitoring_versions()) == vm_total
    assert len(ac_tester.get_backup_versions()) == vm_total
    assert len(ac_tester.get_sharding_entries()) == 1


@mark.e2e_vm_migration_sharded_x509
def test_install_operator(default_operator):
    default_operator.assert_is_running()


@mark.e2e_vm_migration_sharded_x509
def test_migration_dry_run_connectivity_passes(mdb_sharded_migration: MongoDB):
    """Set migration-dry-run annotation and wait for NetworkConnectivityVerification condition (X509 path)."""
    run_migration_dry_run_connectivity_passes(mdb_sharded_migration)


@mark.e2e_vm_migration_sharded_x509
def test_mdb_sharded_reaches_running(mdb_sharded_migration: MongoDB, om_tester: OMTester):
    mdb_sharded_migration.assert_reaches_phase(Phase.Running, timeout=1800)

    ac_tester = om_tester.get_automation_config_tester()
    config_rs_name = f"{mdb_sharded_migration.name}-config"
    vm_shard_rs_name = "vm-shard-0"
    ac_tester.assert_sharded_cluster_processes(config_rs_name, [vm_shard_rs_name], MONGOS_COUNT * 2)


@mark.e2e_vm_migration_sharded_x509
def test_promote_and_prune_config_server(mdb_sharded_migration: MongoDB, om_tester: OMTester):
    try_load(mdb_sharded_migration)
    config_rs_name = f"{mdb_sharded_migration.name}-config"
    for i in range(CONFIG_SERVER_COUNT):
        mdb_sharded_migration["spec"]["memberConfig"][i]["priority"] = "1"
        mdb_sharded_migration["spec"]["memberConfig"][i]["votes"] = 1
        mdb_sharded_migration.update()
        mdb_sharded_migration.assert_reaches_phase(Phase.Running)

        config_external = [
            m for m in mdb_sharded_migration["spec"]["externalMembers"] if m["replicaSetName"] == config_rs_name
        ]
        if config_external:
            mdb_sharded_migration["spec"]["externalMembers"].remove(config_external[-1])
            mdb_sharded_migration.update()
            mdb_sharded_migration.assert_reaches_phase(Phase.Running)

        om_tester.assert_cluster_available(mdb_sharded_migration.name)


@mark.e2e_vm_migration_sharded_x509
def test_promote_and_prune_shard(mdb_sharded_migration: MongoDB, om_tester: OMTester):
    try_load(mdb_sharded_migration)
    vm_shard_rs_name = "vm-shard-0"

    shard_external = [
        m for m in mdb_sharded_migration["spec"]["externalMembers"] if m["replicaSetName"] == vm_shard_rs_name
    ]
    for _ in range(len(shard_external)):
        current = [
            m for m in mdb_sharded_migration["spec"]["externalMembers"] if m["replicaSetName"] == vm_shard_rs_name
        ]
        if not current:
            break
        mdb_sharded_migration["spec"]["externalMembers"].remove(current[-1])
        mdb_sharded_migration.update()
        mdb_sharded_migration.assert_reaches_phase(Phase.Running)
        om_tester.assert_cluster_available(mdb_sharded_migration.name)


@mark.e2e_vm_migration_sharded_x509
def test_prune_mongos(mdb_sharded_migration: MongoDB, mdb_health_checker: MongoDBBackgroundTester):
    try_load(mdb_sharded_migration)

    mongos_external = [m for m in mdb_sharded_migration["spec"]["externalMembers"] if m["type"] == "mongos"]
    for m in mongos_external:
        mdb_sharded_migration["spec"]["externalMembers"].remove(m)
    mdb_sharded_migration.update()
    mdb_sharded_migration.assert_reaches_phase(Phase.Running)

    mdb_health_checker.assert_healthiness()


@mark.e2e_vm_migration_sharded_x509
def test_process_names(namespace: str, om_tester: OMTester, mdb_sharded_migration: MongoDB):
    ac_tester = om_tester.get_automation_config_tester()
    process_names = [process["name"] for process in ac_tester.get_all_processes()]
    name = mdb_sharded_migration.name

    for i in range(mdb_sharded_migration["spec"]["configServerCount"]):
        assert f"k8s/{namespace}/{name}-config-{i}" in process_names

    for i in range(mdb_sharded_migration["spec"]["mongodsPerShardCount"]):
        assert f"k8s/{namespace}/{name}-0-{i}" in process_names

    for i in range(mdb_sharded_migration["spec"]["mongosCount"]):
        assert f"k8s/{namespace}/{name}-mongos-{i}" in process_names
