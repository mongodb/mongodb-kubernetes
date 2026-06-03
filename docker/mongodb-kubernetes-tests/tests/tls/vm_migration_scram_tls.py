"""VM migration E2E: SCRAM auth over TLS transport.

Same flow as vm_migration but mongod runs with requireSSL. This exercises the
MongodTLSCAPath path in the connectivity validator: auth is SCRAM (keyfile) but
the transport layer is TLS, so the validator must configure TLS without a client
certificate to reach the mongod.
"""

import yaml
from kubetester import create_or_update_secret, get_statefulset, read_secret, try_load
from kubetester.certs import ISSUER_CA_NAME, create_mongodb_tls_certs
from kubetester.kubetester import KubernetesTester, fcv_from_version
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB
from kubetester.omtester import OMContext, OMTester
from kubetester.operator import Operator
from kubetester.phase import Phase
from pytest import fixture, mark
from tests.tls.vm_migration_dry_run import run_migration_dry_run_connectivity_passes

VM_STS_NAME = "vm-mongodb-scram-tls"
VM_RS_NAME = "vm-mongodb-scram-tls-rs"
MDB_RESOURCE_NAME = "my-replica-set"

# Paths match operator constants (pkg/util/constants.go: TLSCaMountPath).
SERVER_PEM_PATH = "/mongodb-automation/server.pem"
CUSTOM_CA_PEM_PATH = "/mongodb-automation/tls/ca/ca-pem"


@fixture(scope="module")
def om_tester(namespace: str, operator) -> OMTester:
    config_map = KubernetesTester.read_configmap(namespace, "my-project")
    secret = KubernetesTester.read_secret(namespace, "my-credentials")
    tester = OMTester(OMContext.build_from_config_map_and_secret(config_map, secret))
    tester.ensure_agent_api_key()
    return tester


@fixture(scope="module")
def vm_server_certs(issuer: str, namespace: str) -> str:
    return create_mongodb_tls_certs(ISSUER_CA_NAME, namespace, VM_STS_NAME, f"{VM_STS_NAME}-cert", 3, None, VM_STS_NAME)


@fixture(scope="module")
def vm_tls_pem_secret(namespace: str, vm_server_certs: str) -> str:
    """Single secret with both server.pem (cert+key) and ca.pem for subPath mounting."""
    data = read_secret(namespace, vm_server_certs)
    cert_pem = data.get("tls.crt", b"")
    key_pem = data.get("tls.key", b"")
    ca_pem = data.get("ca.crt", b"")
    if isinstance(cert_pem, bytes):
        cert_pem = cert_pem.decode("utf-8")
    if isinstance(key_pem, bytes):
        key_pem = key_pem.decode("utf-8")
    if isinstance(ca_pem, bytes):
        ca_pem = ca_pem.decode("utf-8")
    create_or_update_secret(namespace, "vm-scram-tls-pem", {"server.pem": cert_pem + key_pem, "ca.pem": ca_pem})
    return "vm-scram-tls-pem"


@fixture(scope="module")
def vm_sts(namespace: str, om_tester: OMTester, vm_tls_pem_secret: str):
    """Deploy VM StatefulSet with TLS cert files mounted via subPath."""
    with open(yaml_fixture("vm_statefulset.yaml"), "r") as f:
        sts_body = yaml.safe_load(f.read())

    sts_body["metadata"]["name"] = VM_STS_NAME
    sts_body["spec"]["serviceName"] = VM_STS_NAME
    sts_body["spec"]["selector"]["matchLabels"]["app"] = VM_STS_NAME
    sts_body["spec"]["template"]["metadata"]["labels"]["app"] = VM_STS_NAME

    sts_body["spec"]["template"]["spec"]["containers"][0]["env"] = [
        {"name": "MMS_GROUP_ID", "value": om_tester.context.project_id},
        {"name": "MMS_BASE_URL", "value": om_tester.context.base_url},
        {"name": "MMS_API_KEY", "value": om_tester.context.agent_api_key},
    ]

    volumes = sts_body["spec"]["template"]["spec"].get("volumes") or []
    volumes.append({"name": "mongodb-certs", "secret": {"secretName": vm_tls_pem_secret}})
    sts_body["spec"]["template"]["spec"]["volumes"] = volumes

    mounts = sts_body["spec"]["template"]["spec"]["containers"][0].get("volumeMounts") or []
    mounts.append({"name": "mongodb-certs", "mountPath": SERVER_PEM_PATH, "subPath": "server.pem", "readOnly": True})
    mounts.append({"name": "mongodb-certs", "mountPath": CUSTOM_CA_PEM_PATH, "subPath": "ca.pem", "readOnly": True})
    sts_body["spec"]["template"]["spec"]["containers"][0]["volumeMounts"] = mounts

    KubernetesTester.create_or_update_statefulset(namespace, body=sts_body)
    return sts_body


@fixture(scope="module")
def vm_service(namespace: str):
    with open(yaml_fixture("vm_service.yaml"), "r") as f:
        service_body = yaml.safe_load(f.read())

    service_body["metadata"]["name"] = VM_STS_NAME
    service_body["spec"]["selector"]["app"] = VM_STS_NAME
    KubernetesTester.create_or_update_service(namespace, body=service_body)
    return service_body


@fixture(scope="module")
def mdb_tls_certs(issuer: str, namespace: str) -> str:
    return create_mongodb_tls_certs(ISSUER_CA_NAME, namespace, MDB_RESOURCE_NAME, f"{MDB_RESOURCE_NAME}-cert")


@fixture(scope="module")
def mdb_migration(
    namespace: str,
    custom_mdb_version: str,
    issuer_ca_configmap: str,
    vm_tls_pem_secret: str,
    mdb_tls_certs: str,
    vm_sts,
    vm_service,
) -> MongoDB:
    resource = MongoDB.from_yaml(yaml_fixture("replica-set.yaml"), namespace=namespace)

    if try_load(resource):
        return resource

    resource.set_version(custom_mdb_version)
    resource["spec"]["replicaSetNameOverride"] = VM_RS_NAME
    resource["spec"]["security"] = {
        "tls": {"enabled": True, "ca": issuer_ca_configmap},
    }

    resource["spec"]["externalMembers"] = []
    for i in range(vm_sts["spec"]["replicas"]):
        resource["spec"]["externalMembers"].append(
            {
                "processName": f"{VM_STS_NAME}-{i}",
                "hostname": f"{VM_STS_NAME}-{i}.{vm_service['metadata']['name']}.{namespace}.svc.cluster.local",
                "type": "mongod",
                "replicaSetName": VM_RS_NAME,
            }
        )

    resource["spec"]["memberConfig"] = []
    for i in range(resource.get_members()):
        resource["spec"]["memberConfig"].append({"votes": 0, "priority": "0"})

    resource.create()
    return resource


@mark.e2e_vm_migration_scram_tls
def test_deploy_vm(namespace: str, vm_sts, vm_service):
    def sts_is_ready():
        sts = get_statefulset(namespace, vm_sts["metadata"]["name"])
        return sts.status.ready_replicas == 3

    KubernetesTester.wait_until(sts_is_ready, timeout=300)


@mark.e2e_vm_migration_scram_tls
def test_configure_ac(namespace: str, om_tester: OMTester, vm_sts, vm_service, custom_mdb_version):
    """Configure VM processes with requireSSL and SCRAM keyfile auth in a single AC push.

    The top-level ac["ssl"] block must be present in the same request as per-process
    sslMode — OM rejects per-process SSL config when the global ssl block is absent.
    clientCertificateMode is OPTIONAL: SCRAM connections do not present a client cert.
    """
    ac = om_tester.api_get_automation_config()
    if len(ac["processes"]) > 0:
        return

    ac["ssl"] = {
        "CAFilePath": CUSTOM_CA_PEM_PATH,
        "clientCertificateMode": "OPTIONAL",
    }

    ac["processes"] = []
    ac["monitoringVersions"] = []
    ac["replicaSets"] = [{"_id": VM_RS_NAME, "members": [], "protocolVersion": "1"}]

    for i in range(vm_sts["spec"]["replicas"]):
        process_name = f"{VM_STS_NAME}-{i}"
        hostname = f"{process_name}.{vm_service['metadata']['name']}.{namespace}.svc.cluster.local"

        ac["monitoringVersions"].append(
            {
                "hostname": hostname,
                "logPath": "/var/log/mongodb-mms-automation/monitoring-agent.log",
                "logRotate": {"sizeThresholdMB": 1000, "timeThresholdHrs": 24},
            }
        )
        ac["processes"].append(
            {
                "version": custom_mdb_version,
                "name": process_name,
                "hostname": hostname,
                "logRotate": {"sizeThresholdMB": 1000, "timeThresholdHrs": 24},
                "authSchemaVersion": 5,
                "featureCompatibilityVersion": fcv_from_version(custom_mdb_version),
                "processType": "mongod",
                "args2_6": {
                    "net": {
                        "port": 27017,
                        "tls": {
                            "mode": "requireSSL",
                            "certificateKeyFile": SERVER_PEM_PATH,
                            "CAFile": CUSTOM_CA_PEM_PATH,
                        },
                    },
                    "storage": {"dbPath": "/data/"},
                    "systemLog": {"path": "/data/mongodb.log", "destination": "file"},
                    "replication": {"replSetName": VM_RS_NAME},
                },
            }
        )
        ac["replicaSets"][0]["members"].append(
            {
                "_id": i + 100,
                "host": process_name,
                "priority": 1,
                "votes": 1,
                "secondaryDelaySecs": 0,
                "hidden": False,
                "arbiterOnly": False,
            }
        )

    om_tester.api_put_automation_config(ac)
    om_tester.wait_agents_ready(timeout=600)


@mark.e2e_vm_migration_scram_tls
def test_migration_dry_run_connectivity_passes(mdb_migration: MongoDB):
    """Validator must reach each VM mongod over TLS using only the CA (no client cert, SCRAM auth)."""
    run_migration_dry_run_connectivity_passes(mdb_migration)


@mark.e2e_vm_migration_scram_tls
def test_install_operator(operator: Operator):
    operator.assert_is_running()


@mark.e2e_vm_migration_scram_tls
def test_mdb_reaches_running(mdb_migration: MongoDB):
    mdb_migration.assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_vm_migration_scram_tls
def test_promote_and_prune(mdb_migration: MongoDB, vm_sts):
    try_load(mdb_migration)
    for i in range(vm_sts["spec"]["replicas"]):
        mdb_migration["spec"]["memberConfig"][i]["priority"] = "1"
        mdb_migration["spec"]["memberConfig"][i]["votes"] = 1
        mdb_migration.update()
        mdb_migration.assert_reaches_phase(Phase.Running)

        mdb_migration["spec"]["externalMembers"].pop()
        mdb_migration.update()
        mdb_migration.assert_reaches_phase(Phase.Running)
