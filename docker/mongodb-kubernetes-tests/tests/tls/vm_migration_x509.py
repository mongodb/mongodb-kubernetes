"""
VM migration E2E tests with X509 and TLS.
MDB and pseudo-VM AC use TLS and X509 client auth (deploymentAuthMechanisms); internal cluster
auth between mongod processes continues to use keyFile (SCRAM-SHA-256 for __system@local).

Flow: TLS → X509 client auth (fixtures bring VM agents to goal state), then migrate (MDB resource
with externalMembers pointing at VM processes, promote K8s members and prune VM members).

The MDB resource is created with X509 + clientCertificateSecretRef from the start, referencing
the same agent cert the VM agents use. The operator mounts this cert on K8s pods and sets
autoPEMKeyFilePath to the same hash-based path already mounted on VM pods, so all agents share
the same cert without any auth-disable window.

Non-static database pods: when spec.externalMembers is non-empty, the operator forwards
deployment.agentVersion.name from Ops Manager as MDB_AGENT_VERSION so downloaded agents match
the version already pinned in the automation config. VM setup therefore sets agentVersion.name
to the same tag as the pseudo-VM agent image (AGENT_IMAGE); static architecture ignores
MDB_AGENT_VERSION (bundled agent in the image).
"""

import base64
import hashlib
import json
import yaml
from cryptography import x509 as crypto_x509
from cryptography.hazmat.backends import default_backend
from kubetester import create_or_update_secret, get_statefulset, read_secret, try_load
from kubetester.certs import ISSUER_CA_NAME, create_mongodb_tls_certs, create_x509_agent_tls_certs
from kubetester.kubetester import KubernetesTester, fcv_from_version
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB
from kubetester.omtester import OMContext, OMTester
from kubetester.phase import Phase
from pytest import fixture, mark

VM_STS_NAME = "vm-mongodb"
VM_RS_NAME = "vm-mongodb-rs"
MDB_RESOURCE_NAME = "my-replica-set"
SERVER_PEM_PATH = "/mongodb-automation/server.pem"
# Paths match operator constants (pkg/util/constants.go: TLSCaMountPath, AgentCertMountPath).
CUSTOM_CA_PEM_PATH = "/mongodb-automation/tls/ca/ca-pem"

# Custom cert path used for VM agents — intentionally different from the operator's default
# AgentCertMountPath to test that the operator preserves an arbitrary existing autoPEMKeyFilePath
# and creates a matching mount on K8s pods rather than overwriting with its own hash-based path.
CUSTOM_AGENT_CERT_DIR = "/var/lib/mongodb-mms-automation/certs"
CUSTOM_AGENT_CERT_FILENAME = "agent.pem"
CUSTOM_AGENT_CERT_PATH = f"{CUSTOM_AGENT_CERT_DIR}/{CUSTOM_AGENT_CERT_FILENAME}"


def _operator_pem_hash(cert_pem: str, key_pem: str) -> str:
    """Compute the hash the operator uses for autoPEMKeyFilePath.

    Replicates controllers/operator/pem/pem_collection.go ReadHashFromData() for a
    kubernetes.io/tls secret with tls.crt and tls.key entries:

      PemFiles = {
        "tls.crt": {privateKey: "", certificate: cert_pem},
        "tls.key": {privateKey: key_pem, certificate: ""},
      }
      hash = Base32(SHA256(json.Marshal(PemFiles)))

    Go json.Marshal sorts map keys alphabetically and serializes struct fields in declaration
    order (privateKey before certificate). Python 3.7+ dicts preserve insertion order so
    json.dumps respects the order below.
    """
    payload = {
        "tls.crt": {"privateKey": "", "certificate": cert_pem},
        "tls.key": {"privateKey": key_pem, "certificate": ""},
    }
    json_bytes = json.dumps(payload, separators=(",", ":")).encode()
    digest = hashlib.sha256(json_bytes).digest()
    return base64.b32encode(digest).decode("ascii").rstrip("=")


@fixture(scope="module")
def om_tester(namespace: str, operator) -> OMTester:
    config_map = KubernetesTester.read_configmap(namespace, "my-project")
    secret = KubernetesTester.read_secret(namespace, "my-credentials")
    tester = OMTester(OMContext.build_from_config_map_and_secret(config_map, secret))
    tester.ensure_agent_api_key()
    return tester


@fixture(scope="module")
def vm_server_certs(issuer: str, namespace: str):
    """TLS certs for VM mongod processes (hostnames vm-mongodb-0, vm-mongodb-1, vm-mongodb-2)."""
    return create_mongodb_tls_certs(ISSUER_CA_NAME, namespace, VM_STS_NAME, f"{VM_STS_NAME}-cert", 3, None, VM_STS_NAME)


@fixture(scope="module")
def vm_agent_certs(issuer: str, namespace: str) -> str:
    return create_x509_agent_tls_certs(issuer, namespace, MDB_RESOURCE_NAME)


@fixture(scope="module")
def vm_agent_combined_pem(namespace: str, vm_agent_certs: str) -> tuple:
    """Pre-create the operator-format combined PEM secret (agent-certs-pem) for autoPEMKeyFilePath.

    The operator creates this secret via VerifyAndEnsureClientCertificatesForAgentsAndTLSType when
    it reconciles a MDB resource with clientCertificateSecretRef. We pre-create it here so that VM
    pods can mount it before the MDB resource exists (VM RS setup happens first).

    The secret name and hash formula exactly match what the operator produces from the
    kubernetes.io/tls secret, so when the operator reconciles it will update the secret with the
    same content (adding a latest-hash annotation key, which is harmless).

    Returns (secret_name, subject_dn, agent_hash).
    """
    data = read_secret(namespace, vm_agent_certs)
    cert_pem = data.get("tls.crt", b"")
    key_pem = data.get("tls.key", b"")
    if isinstance(cert_pem, bytes):
        cert_pem = cert_pem.decode("utf-8")
    if isinstance(key_pem, bytes):
        key_pem = key_pem.decode("utf-8")

    # Extract subject DN from cert (MongoDB autoUser format: RFC4514, most-specific-first).
    cert_obj = crypto_x509.load_pem_x509_certificate(cert_pem.encode(), default_backend())
    subject_dn = cert_obj.subject.rfc4514_string()

    agent_hash = _operator_pem_hash(cert_pem, key_pem)

    # Combined PEM matches VerifyTLSSecretForStatefulSet: cert (with trailing \n) + key.
    if not cert_pem.endswith("\n"):
        cert_pem += "\n"
    combined_pem = cert_pem + key_pem

    # Secret name matches what the operator derives: <clientCertificateSecretRef>-pem.
    pem_secret_name = f"{vm_agent_certs}-pem"
    create_or_update_secret(namespace, pem_secret_name, {agent_hash: combined_pem})
    return pem_secret_name, subject_dn, agent_hash


@fixture(scope="module")
def vm_server_combined_pem(namespace: str, vm_server_certs: str) -> str:
    """Create a combined cert+key PEM secret for certificateKeyFile (MongoDB requires both in one file)."""
    data = read_secret(namespace, vm_server_certs)
    cert_pem = data.get("tls.crt", b"")
    key_pem = data.get("tls.key", b"")
    if isinstance(cert_pem, bytes):
        cert_pem = cert_pem.decode("utf-8")
    if isinstance(key_pem, bytes):
        key_pem = key_pem.decode("utf-8")
    create_or_update_secret(namespace, "vm-mongodb-server-pem", {"server.pem": cert_pem + key_pem})
    return "vm-mongodb-server-pem"


@fixture(scope="module")
def vm_sts(
    namespace: str, om_tester: OMTester, vm_server_certs: str, vm_agent_combined_pem: tuple, vm_server_combined_pem: str
):
    """Deploy VM StatefulSet with cert volumes (server combined PEM + CA + agent PEM)."""
    with open(yaml_fixture("vm_statefulset.yaml"), "r") as f:
        sts_body = yaml.safe_load(f.read())

    sts_body["spec"]["template"]["spec"]["containers"][0]["env"] = [
        {"name": "MMS_GROUP_ID", "value": om_tester.context.project_id},
        {"name": "MMS_BASE_URL", "value": om_tester.context.base_url},
        {"name": "MMS_API_KEY", "value": om_tester.context.agent_api_key},
    ]

    volumes = sts_body["spec"]["template"]["spec"].get("volumes") or []
    # Combined cert+key PEM — MongoDB certificateKeyFile requires both in one file.
    volumes.append(
        {
            "name": "mongodb-certs",
            "secret": {
                "secretName": vm_server_combined_pem,
                "items": [{"key": "server.pem", "path": "server.pem"}],
            },
        }
    )
    # CA cert mounted at the same path the operator sets in tls.CAFilePath
    # (/mongodb-automation/tls/ca/ca-pem) so VM agents remain functional after operator reconcile.
    volumes.append(
        {
            "name": "ca-cert",
            "secret": {
                "secretName": "ca-key-pair",
                "items": [{"key": "tls.crt", "path": "ca-pem"}],
            },
        }
    )
    # Mount the agent cert at CUSTOM_AGENT_CERT_PATH — a path intentionally different from the
    # operator's default AgentCertMountPath — to exercise the operator's AgentCertExternalPath
    # code path. The items mapping places the pre-created cert (key=python_hash) at the fixed
    # filename CUSTOM_AGENT_CERT_FILENAME inside CUSTOM_AGENT_CERT_DIR. The operator will read
    # autoPEMKeyFilePath=CUSTOM_AGENT_CERT_PATH from the AC, preserve it, and mount the cert on
    # K8s pods using items: [{key: GO_HASH, path: CUSTOM_AGENT_CERT_FILENAME}] at CUSTOM_AGENT_CERT_DIR.
    agent_secret_name, _, agent_hash = vm_agent_combined_pem
    volumes.append(
        {
            "name": "agent-cert",
            "secret": {
                "secretName": agent_secret_name,
                "items": [{"key": agent_hash, "path": CUSTOM_AGENT_CERT_FILENAME}],
            },
        }
    )
    sts_body["spec"]["template"]["spec"]["volumes"] = volumes

    mounts = sts_body["spec"]["template"]["spec"]["containers"][0].get("volumeMounts") or []
    mounts.append({"name": "mongodb-certs", "mountPath": "/mongodb-automation", "readOnly": True})
    mounts.append({"name": "ca-cert", "mountPath": "/mongodb-automation/tls/ca", "readOnly": True})
    mounts.append({"name": "agent-cert", "mountPath": CUSTOM_AGENT_CERT_DIR, "readOnly": True})
    sts_body["spec"]["template"]["spec"]["containers"][0]["volumeMounts"] = mounts

    KubernetesTester.create_or_update_statefulset(namespace, body=sts_body)
    return sts_body


@fixture(scope="module")
def mdb_tls_certs(issuer: str, namespace: str):
    return create_mongodb_tls_certs(ISSUER_CA_NAME, namespace, MDB_RESOURCE_NAME, f"{MDB_RESOURCE_NAME}-cert")


@fixture(scope="module")
def mdb_migration(
    namespace: str,
    custom_mdb_version: str,
    issuer_ca_configmap: str,
    mdb_tls_certs: str,
    vm_sts,
    vm_service,
    vm_agent_certs: str,
) -> MongoDB:
    """MDB with TLS + X509 and externalMembers pointing at VM hostnames.

    Uses clientCertificateSecretRef to reference the same agent cert the VM agents use.
    The operator mounts this cert on K8s pods and sets autoPEMKeyFilePath to the same
    hash-based path, so all agents (VM + K8s) share the cert without any auth-disable window.

    Non-empty externalMembers triggers the operator to pin agent downloads to
    automationConfig.agentVersion.name (must stay aligned with the VM agent image tag set in AC).
    """
    resource = MongoDB.from_yaml(yaml_fixture("replica-set.yaml"), namespace=namespace)

    if try_load(resource):
        return resource

    resource.set_version(custom_mdb_version)
    resource["spec"]["replicaSetNameOverride"] = VM_RS_NAME
    resource["spec"]["security"] = {
        "tls": {"enabled": True, "ca": issuer_ca_configmap},
        "authentication": {
            "enabled": True,
            "modes": ["X509"],
            "agents": {
                "mode": "X509",
                "clientCertificateSecretRef": {"name": vm_agent_certs},
            },
        },
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
    resource.update()
    return resource


@fixture(scope="module")
def vm_service(namespace: str):
    with open(yaml_fixture("vm_service.yaml"), "r") as f:
        service_body = yaml.safe_load(f.read())
    KubernetesTester.create_or_update_service(namespace, body=service_body)
    return service_body


def _build_processes(vm_sts: dict, vm_service: dict, namespace: str, custom_mdb_version: str, tls: bool) -> tuple:
    """Build processes, monitoringVersions, and replicaSet members for the AC.

    tls: enable requireTLS + certificateKeyFile on the process.
    Internal cluster auth always uses keyFile (SCRAM-SHA-256 for __system@local);
    clusterAuthMode: x509 is intentionally NOT set to avoid the rolling sendKeyFile→sendX509
    transition which breaks replication in mixed-mode clusters.
    """
    processes = []
    monitoring_versions = []
    members = []
    for i in range(vm_sts["spec"]["replicas"]):
        process_name = f"{VM_STS_NAME}-{i}"
        hostname = f"{process_name}.{vm_service['metadata']['name']}.{namespace}.svc.cluster.local"
        mon_entry = {
            "hostname": hostname,
            "logPath": "/var/log/mongodb-mms-automation/monitoring-agent.log",
            "logRotate": {"sizeThresholdMB": 1000, "timeThresholdHrs": 24},
        }
        if tls:
            mon_entry["additionalParams"] = {
                "sslTrustedServerCertificates": CUSTOM_CA_PEM_PATH,
                "useSslForAllConnections": "true",
            }
        monitoring_versions.append(mon_entry)
        net = {"port": 27017}
        if tls:
            net["tls"] = {
                "mode": "requireTLS",
                "certificateKeyFile": SERVER_PEM_PATH,
            }
        else:
            net["tls"] = {"mode": "disabled"}
        process = {
            "version": custom_mdb_version,
            "name": process_name,
            "hostname": hostname,
            "logRotate": {"sizeThresholdMB": 1000, "timeThresholdHrs": 24},
            "authSchemaVersion": 5,
            "featureCompatibilityVersion": fcv_from_version(custom_mdb_version),
            "processType": "mongod",
            "args2_6": {
                "net": net,
                "storage": {"dbPath": "/data/"},
                "systemLog": {"path": "/data/mongodb.log", "destination": "file"},
                "replication": {"replSetName": VM_RS_NAME},
            },
        }
        processes.append(process)
        members.append(
            {
                "_id": i + 100,  # Avoid conflicts with member IDs assigned by operator to K8s pods (0, 1, 2)
                "host": process_name,
                "priority": 1,
                "votes": 1,
                "secondaryDelaySecs": 0,
                "hidden": False,
                "arbiterOnly": False,
            }
        )
    return processes, monitoring_versions, members


@mark.e2e_vm_migration_x509
def test_vm_mdb_reaches_running(namespace: str, vm_sts, vm_service):
    def sts_is_ready():
        sts = get_statefulset(namespace, vm_sts["metadata"]["name"])
        return sts.status.ready_replicas == 3

    KubernetesTester.wait_until(sts_is_ready, timeout=300)


@mark.e2e_vm_migration_x509
def test_vm_ac_no_auth(om_tester: OMTester, vm_sts: dict, vm_service: dict, namespace: str, custom_mdb_version: str):
    """Step 1: start VM replica set without auth or TLS so the replica set forms and agents register."""
    ac = om_tester.api_get_automation_config()
    if len(ac["processes"]) > 0:
        return

    processes, monitoring_versions, members = _build_processes(
        vm_sts, vm_service, namespace, custom_mdb_version, tls=False
    )
    ac["processes"] = processes
    ac["monitoringVersions"] = monitoring_versions
    ac["replicaSets"] = [{"_id": VM_RS_NAME, "members": members, "protocolVersion": "1"}]
    om_tester.api_put_automation_config(ac)
    om_tester.wait_agents_ready(timeout=600)


@mark.e2e_vm_migration_x509
def test_vm_ac_tls(
    om_tester: OMTester,
    vm_sts: dict,
    vm_service: dict,
    namespace: str,
    custom_mdb_version: str,
):
    """Step 2: enable TLS on running replica set (auth still disabled).
    OM rejects simultaneous TLS mode + auth changes, so TLS must be a separate step.
    """
    ac = om_tester.api_get_automation_config()
    tls_mode = ac.get("processes", [{}])[0].get("args2_6", {}).get("net", {}).get("tls", {}).get("mode")
    if tls_mode == "requireTLS":
        return

    # Use CUSTOM_AGENT_CERT_PATH — the path VM agents have their cert mounted at.
    # The operator will read this from the AC when reconciling the MDB resource with
    # externalMembers and preserve it (AgentCertExternalPath), mounting K8s pods at the same path.
    ac["tls"] = {
        "CAFilePath": CUSTOM_CA_PEM_PATH,
        "autoPEMKeyFilePath": CUSTOM_AGENT_CERT_PATH,
        "clientCertificateMode": "REQUIRE",
    }
    processes, monitoring_versions, _ = _build_processes(vm_sts, vm_service, namespace, custom_mdb_version, tls=True)
    ac["processes"] = processes
    ac["monitoringVersions"] = monitoring_versions
    om_tester.api_put_automation_config(ac)
    om_tester.wait_agents_ready(timeout=600)


@mark.e2e_vm_migration_x509
def test_vm_ac_x509_auth(
    om_tester: OMTester,
    vm_sts: dict,
    vm_service: dict,
    namespace: str,
    custom_mdb_version: str,
    vm_agent_combined_pem: tuple,
):
    """Step 3: enable X509 client auth (processes TLS config unchanged).
    Agent connects while TLS is on but auth is still disabled, creates the $external user for
    autoUser, then restarts mongod with auth enabled.
    Internal cluster auth continues to use keyFile (SCRAM-SHA-256 for __system@local).
    """
    ac = om_tester.api_get_automation_config()
    if ac.get("auth", {}).get("disabled", True) is False:
        return

    _, agent_subject_dn, _ = vm_agent_combined_pem
    ac["auth"] = {
        "disabled": False,
        "authoritativeSet": True,
        "autoUser": agent_subject_dn,
        "autoAuthMechanism": "MONGODB-X509",
        "autoAuthMechanisms": ["MONGODB-X509"],
        "autoAuthRestrictions": [],
        "deploymentAuthMechanisms": ["MONGODB-X509"],
        "keyfile": "/var/lib/mongodb-mms-automation/keyfile",
        "keyfileWindows": "%SystemDrive%\\MMSAutomation\\versions\\keyfile",
        "key": "dGVzdC1rZXlmaWxlLWNvbnRlbnQtZm9yLXZtLW1pZ3JhdGlvbi14NTA5",
        "usersWanted": [],
        "usersDeleted": [],
    }
    om_tester.api_put_automation_config(ac)
    om_tester.wait_agents_ready(timeout=600)


@mark.e2e_vm_migration_x509
def test_k8s_mdb_reaches_running(mdb_migration: MongoDB):
    mdb_migration.assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_vm_migration_x509
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
