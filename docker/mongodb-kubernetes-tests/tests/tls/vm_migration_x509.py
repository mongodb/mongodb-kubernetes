"""
VM migration E2E tests with X509 and TLS.
MDB and pseudo-VM AC use TLS and X509 client auth (deploymentAuthMechanisms); internal cluster
auth between mongod processes continues to use keyFile.

Flow: TLS → X509 client auth (fixtures bring VM agents to goal state), then migrate (MDB resource
with externalMembers pointing at VM processes, promote K8s members and prune VM members).

Test order matches ``vm_migration`` (non-TLS): VM ready → AC updates → operator running → MDB
Running → migration dry-run → promote/prune.

The MDB resource is created with X509 + clientCertificateSecretRef and
spec.security.authentication.agents.autoPEMKeyFilePath set to CUSTOM_AGENT_CERT_PATH, matching
the VM pod mount. The operator configures OM and K8s pods to use that path explicitly.

Why different Secrets for the same agent cert+key?
- clientCertificateSecretRef (TLS: tls.crt / tls.key): CRD and operator expect this shape to
  verify, merge, and publish PEM material to Ops Manager.
- vm-agent-cert-pem (single combined file): VM pods must exist and reach goal state *before* any
  MDB exists, so the operator never created *-pem for them. OM also expects autoPEMKeyFilePath to
  be *one file* (cert+key), not separate tls.crt/tls.key paths. The test therefore synthesizes a
  minimal Secret + mount the VM StatefulSet can use without reimplementing operator hash-key layout.

"""

import yaml
from cryptography import x509 as crypto_x509
from cryptography.hazmat.backends import default_backend
from kubetester import create_or_update_secret, get_statefulset, read_secret, try_load
from kubetester.certs import ISSUER_CA_NAME, create_mongodb_tls_certs, create_x509_agent_tls_certs
from kubetester.kubetester import KubernetesTester, fcv_from_version
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB
from kubetester.omtester import OMContext, OMTester
from kubetester.operator import Operator
from kubetester.phase import Phase
from pytest import fixture, mark
from tests.tls.vm_migration_dry_run import (
    run_migration_dry_run_connectivity_passes,
    wait_until_running_and_migration_in_progress,
)
from tests.tls.vm_migration_promote_prune import promote_and_prune_members

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
    """Create a combined PEM secret for VM pod cert mount and extract the agent subject DN.

    Why not mount vm_agent_certs (tls.crt / tls.key) directly on VM pods?
    Ops Manager's tls.autoPEMKeyFilePath points at a single PEM path; the agent reads one file.
    Split TLS keys do not match that contract without extra init logic in the pod.

    Why not reuse the operator's {vm_agent_certs}-pem Secret on VM pods?
    That Secret is created when the MDB reconciles; VM agents must already be using X509 while
    externalMembers still point at VM processes, often before the MDB CR exists or finishes
    reconciling. This fixture is the bootstrap path for VM-only phases of the test.

    Why a trivial Secret shape (one key = filename)?
    VM StatefulSet is hand-built YAML; a directory mount of one key → one file avoids copying the
    operator's hash-keyed layout in test code.

    Returns (secret_name, subject_dn).
    """
    data = read_secret(namespace, vm_agent_certs)
    cert_pem = data.get("tls.crt", b"")
    key_pem = data.get("tls.key", b"")
    if isinstance(cert_pem, bytes):
        cert_pem = cert_pem.decode("utf-8")
    if isinstance(key_pem, bytes):
        key_pem = key_pem.decode("utf-8")

    cert_obj = crypto_x509.load_pem_x509_certificate(cert_pem.encode(), default_backend())
    subject_dn = cert_obj.subject.rfc4514_string()

    pem_secret_name = "vm-agent-cert-pem"
    create_or_update_secret(namespace, pem_secret_name, {CUSTOM_AGENT_CERT_FILENAME: cert_pem + key_pem})
    return pem_secret_name, subject_dn


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
    namespace: str,
    om_tester: OMTester,
    vm_server_certs: str,
    vm_agent_combined_pem: tuple[str, str],
    vm_server_combined_pem: str,
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
    # Why vm-agent-cert-pem instead of the TLS secret or *-pem here: see vm_agent_combined_pem.
    # Why this mountPath: OM tls.autoPEMKeyFilePath and later MDB autoPEMKeyFilePath must name the
    # same path so VM and K8s agents agree with the automation config during migration.
    agent_secret_name, _ = vm_agent_combined_pem
    volumes.append({"name": "agent-cert", "secret": {"secretName": agent_secret_name}})
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
def mdb_k8s(
    namespace: str,
    custom_mdb_version: str,
    issuer_ca_configmap: str,
    mdb_tls_certs: str,
    vm_sts,
    vm_service,
    vm_agent_certs: str,
) -> MongoDB:
    """MDB with TLS + X509, externalMembers, and agents.autoPEMKeyFilePath = CUSTOM_AGENT_CERT_PATH.

    Non-empty externalMembers triggers the operator to pin non-static agent downloads (MDB_AGENT_VERSION).
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
                # Why: CRD requires a kubernetes.io/tls source; operator validates and derives *-pem.
                "clientCertificateSecretRef": {"name": vm_agent_certs},
                # Why: tells OM/agents where the combined PEM lives on every pod; must equal VM mount so
                # externalMembers (VM) and in-cluster members use one automation config.
                "autoPEMKeyFilePath": CUSTOM_AGENT_CERT_PATH,
            },
        },
    }
    # Start with 0 k8s replicas. Extending begins when the user increments spec.members
    # to add k8s pods alongside the VM members.
    resource["spec"]["members"] = 0

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

    # VM agents use CUSTOM_AGENT_CERT_PATH; the MDB CR sets the same path via agents.autoPEMKeyFilePath.
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
    vm_agent_combined_pem: tuple[str, str],
):
    """Step 3: enable X509 client auth (processes TLS config unchanged).
    Agent connects while TLS is on but auth is still disabled, creates the $external user for
    autoUser, then restarts mongod with auth enabled.
    Internal cluster auth continues to use keyFile (SCRAM-SHA-256 for __system@local).
    """
    ac = om_tester.api_get_automation_config()
    if ac.get("auth", {}).get("disabled", True) is False:
        return

    _, agent_subject_dn = vm_agent_combined_pem
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
def test_install_operator(operator: Operator):
    operator.assert_is_running()


@mark.e2e_vm_migration
def test_k8s_mdb_reaches_running(mdb_k8s: MongoDB):
    mdb_k8s.assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_vm_migration_x509
def test_migration_dry_run_connectivity_passes(mdb_k8s: MongoDB):
    """Dry-run under TLS+X509; operator records result on ``status.migration`` before annotation is cleared."""
    run_migration_dry_run_connectivity_passes(mdb_k8s)


# TODO insert sample data, assert it is still there after migration
@mark.e2e_vm_migration_x509
def test_promote_and_prune(mdb_k8s: MongoDB, vm_sts):
    promote_and_prune_members(mdb_k8s, vm_sts)
