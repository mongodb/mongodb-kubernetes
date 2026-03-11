"""
VM migration E2E tests with X509 and TLS.
MDB and pseudo-VM AC use TLS and X509 client auth (deploymentAuthMechanisms); internal cluster
auth between mongod processes continues to use keyFile (SCRAM-SHA-256 for __system@local).

Flow: TLS → X509 client auth (fixtures bring VM agents to goal state), then migrate (MDB resource
with externalMembers, dry-run, promote and prune).
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
from kubetester.phase import Phase
from pytest import fixture, mark

VM_STS_NAME = "vm-mongodb"
VM_RS_NAME = "vm-mongodb-rs"
MDB_RESOURCE_NAME = "my-replica-set"
SERVER_PEM_PATH = "/mongodb-automation/server.pem"
CA_PEM_PATH = "/mongodb-ca/ca.pem"
AGENT_PEM_PATH = "/mongodb-agent/agent.pem"
AGENT_COMBINED_PEM_SECRET_NAME = "agent-certs-combined"


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
    """Create a combined cert+key PEM secret for autoPEMKeyFilePath, and extract the agent cert subject DN."""
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

    combined_pem = cert_pem + key_pem
    create_or_update_secret(namespace, AGENT_COMBINED_PEM_SECRET_NAME, {"agent.pem": combined_pem})
    return AGENT_COMBINED_PEM_SECRET_NAME, subject_dn


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
    # CA cert at a separate path (can't subPath into an already-mounted directory).
    volumes.append(
        {
            "name": "ca-cert",
            "secret": {
                "secretName": "ca-key-pair",
                "items": [{"key": "tls.crt", "path": "ca.pem"}],
            },
        }
    )
    # Agent combined PEM (cert+key) for autoPEMKeyFilePath.
    agent_secret_name, _ = vm_agent_combined_pem
    volumes.append(
        {
            "name": "agent-cert",
            "secret": {
                "secretName": agent_secret_name,
                "items": [{"key": "agent.pem", "path": "agent.pem"}],
            },
        }
    )
    sts_body["spec"]["template"]["spec"]["volumes"] = volumes

    mounts = sts_body["spec"]["template"]["spec"]["containers"][0].get("volumeMounts") or []
    mounts.append({"name": "mongodb-certs", "mountPath": "/mongodb-automation", "readOnly": True})
    mounts.append({"name": "ca-cert", "mountPath": "/mongodb-ca", "readOnly": True})
    mounts.append({"name": "agent-cert", "mountPath": "/mongodb-agent", "readOnly": True})
    sts_body["spec"]["template"]["spec"]["containers"][0]["volumeMounts"] = mounts

    KubernetesTester.create_or_update_statefulset(namespace, body=sts_body)
    return sts_body


@fixture(scope="module")
def mdb_migration(
    namespace: str,
    custom_mdb_version: str,
    issuer_ca_configmap: str,
) -> MongoDB:
    """MDB with TLS + X509 and externalMembers pointing at VM hostnames. Created after X509 is in use (vm_replica_set_x509_ready)."""
    resource = MongoDB.from_yaml(yaml_fixture("replica-set.yaml"), namespace=namespace)

    if try_load(resource):
        return resource

    resource.set_version(custom_mdb_version)
    resource["spec"]["replicaSetNameOverride"] = VM_RS_NAME
    resource["spec"]["security"] = {
        "tls": {"ca": issuer_ca_configmap},
        "authentication": {
            "enabled": True,
            "modes": ["X509"],
        },
    }
    resource["spec"]["externalMembers"] = [f"{VM_STS_NAME}-0", f"{VM_STS_NAME}-1", f"{VM_STS_NAME}-2"]
    resource["spec"]["memberConfig"] = []
    for i in range(resource.get_members()):
        resource["spec"]["memberConfig"].append({"votes": 0, "priority": "0"})
    resource.create()
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
                "sslTrustedServerCertificates": CA_PEM_PATH,
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
                "_id": i,
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
def test_deploy_vm(namespace: str, vm_sts, vm_service):
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

    agent_image_tag = vm_sts["spec"]["template"]["spec"]["containers"][0]["image"].split(":")[-1]
    if "agentVersion" in ac:
        ac["agentVersion"]["name"] = agent_image_tag

    processes, monitoring_versions, members = _build_processes(
        vm_sts, vm_service, namespace, custom_mdb_version, tls=False
    )
    ac["processes"] = processes
    ac["monitoringVersions"] = monitoring_versions
    ac["replicaSets"] = [{"_id": VM_RS_NAME, "members": members, "protocolVersion": "1"}]
    om_tester.api_put_automation_config(ac)
    om_tester.wait_agents_ready(timeout=600)


@mark.e2e_vm_migration_x509
def test_vm_ac_tls(om_tester: OMTester, vm_sts: dict, vm_service: dict, namespace: str, custom_mdb_version: str):
    """Step 2: enable TLS on running replica set (auth still disabled).
    OM rejects simultaneous TLS mode + auth changes, so TLS must be a separate step.
    """
    ac = om_tester.api_get_automation_config()
    tls_mode = ac.get("processes", [{}])[0].get("args2_6", {}).get("net", {}).get("tls", {}).get("mode")
    if tls_mode == "requireTLS":
        return

    ac["tls"] = {
        "CAFilePath": CA_PEM_PATH,
        "autoPEMKeyFilePath": AGENT_PEM_PATH,
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
def test_mdb_reaches_running(mdb_migration: MongoDB):
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
