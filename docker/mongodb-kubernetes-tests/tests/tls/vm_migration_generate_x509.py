"""
VM migration test with X509-only authentication and keyFile internal cluster auth.

Multi-step AC setup (required because agent uses MONGODB-X509):
  1. Deploy VM RS without auth or TLS (agents register, RS forms)
  2. Enable TLS on the running RS (OM rejects simultaneous TLS + auth changes)
  3. Enable X509 auth (agent authenticates with X509 cert)

Then runs ``migrate generate`` to produce the CR and verifies:
  - spec.security.tls.enabled: true
  - spec.security.authentication.modes: [X509]
  - No internalCluster field (keyFile is the default)
  - Full promote-and-prune lifecycle
"""

import yaml
from cryptography import x509 as crypto_x509
from cryptography.hazmat.backends import default_backend
from kubetester import create_or_update_secret, get_statefulset, read_secret, try_load
from kubetester.certs import ISSUER_CA_NAME, create_mongodb_tls_certs, create_x509_agent_tls_certs
from kubetester.kubetester import KubernetesTester, ensure_ent_version, fcv_from_version
from kubetester.mongodb import MongoDB
from kubetester.omtester import OMContext, OMTester
from kubetester.operator import Operator
from kubetester.phase import Phase
from pytest import fixture, mark
from tests.tls.vm_migration_helpers import (
    deploy_vm_service,
    log_automation_config,
    log_automation_config_diff,
    run_migrate_generate,
)

RS_NAME = "vm-mongodb-rs"
VM_STS_NAME = "vm-mongodb"
VM_SVC_NAME = "vm-mongodb"
SERVER_PEM_PATH = "/mongodb-automation/server.pem"
CA_PEM_PATH = "/mongodb-ca/ca.pem"
AGENT_PEM_PATH = "/mongodb-agent/agent.pem"
OPERATOR_CERT_SECRET = f"{RS_NAME}-cert"
AGENT_COMBINED_PEM_SECRET = "agent-certs-combined"


# ---------------------------------------------------------------------------
# Fixtures
# ---------------------------------------------------------------------------

@fixture(scope="module")
def om_tester(namespace: str) -> OMTester:
    config_map = KubernetesTester.read_configmap(namespace, "my-project")
    secret = KubernetesTester.read_secret(namespace, "my-credentials")
    tester = OMTester(OMContext.build_from_config_map_and_secret(config_map, secret))
    tester.ensure_agent_api_key()
    return tester


@fixture(scope="module")
def vm_server_certs(issuer: str, namespace: str):
    """TLS certs for VM mongod processes."""
    return create_mongodb_tls_certs(
        ISSUER_CA_NAME, namespace, VM_STS_NAME, f"{VM_STS_NAME}-cert", 3, None, VM_STS_NAME,
    )


@fixture(scope="module")
def vm_agent_certs(issuer: str, namespace: str) -> str:
    return create_x509_agent_tls_certs(issuer, namespace, RS_NAME)


@fixture(scope="module")
def vm_agent_combined_pem(namespace: str, vm_agent_certs: str) -> tuple:
    """Combined cert+key PEM for autoPEMKeyFilePath; also extracts the agent subject DN."""
    data = read_secret(namespace, vm_agent_certs)
    cert_pem = data.get("tls.crt", b"")
    key_pem = data.get("tls.key", b"")
    if isinstance(cert_pem, bytes):
        cert_pem = cert_pem.decode("utf-8")
    if isinstance(key_pem, bytes):
        key_pem = key_pem.decode("utf-8")

    cert_obj = crypto_x509.load_pem_x509_certificate(cert_pem.encode(), default_backend())
    subject_dn = cert_obj.subject.rfc4514_string()

    combined_pem = cert_pem + key_pem
    create_or_update_secret(namespace, AGENT_COMBINED_PEM_SECRET, {"agent.pem": combined_pem})
    return AGENT_COMBINED_PEM_SECRET, subject_dn


@fixture(scope="module")
def vm_server_combined_pem(namespace: str, vm_server_certs: str) -> str:
    """Combined cert+key PEM secret for mongod certificateKeyFile."""
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
    vm_agent_combined_pem: tuple,
    vm_server_combined_pem: str,
):
    """Deploy VM StatefulSet with cert volumes (server PEM + CA + agent PEM)."""
    from kubetester.kubetester import fixture as yaml_fixture

    with open(yaml_fixture("vm_statefulset.yaml"), "r") as f:
        sts_body = yaml.safe_load(f.read())

    sts_body["spec"]["template"]["spec"]["containers"][0]["env"] = [
        {"name": "MMS_GROUP_ID", "value": om_tester.context.project_id},
        {"name": "MMS_BASE_URL", "value": om_tester.context.base_url},
        {"name": "MMS_API_KEY", "value": om_tester.context.agent_api_key},
    ]

    volumes = sts_body["spec"]["template"]["spec"].get("volumes") or []
    volumes.append(
        {
            "name": "mongodb-certs",
            "secret": {
                "secretName": vm_server_combined_pem,
                "items": [{"key": "server.pem", "path": "server.pem"}],
            },
        }
    )
    volumes.append(
        {
            "name": "ca-cert",
            "secret": {
                "secretName": "ca-key-pair",
                "items": [{"key": "tls.crt", "path": "ca.pem"}],
            },
        }
    )
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
def vm_service(namespace: str):
    return deploy_vm_service(namespace)


@fixture(scope="module")
def operator_server_certs(issuer: str, namespace: str):
    """TLS certs for operator-managed pods (post-migration)."""
    return create_mongodb_tls_certs(ISSUER_CA_NAME, namespace, RS_NAME, OPERATOR_CERT_SECRET, replicas=3)


def _build_processes(vm_sts, vm_service, namespace, mdb_version, tls):
    """Build processes, monitoringVersions, and RS members.

    Internal cluster auth always uses keyFile (no clusterAuthMode: x509).
    """
    mdb_version = ensure_ent_version(mdb_version)
    processes = []
    monitoring_versions = []
    members = []
    sts_name = vm_sts["metadata"]["name"]
    svc_name = vm_service["metadata"]["name"]
    rs_name = f"{sts_name}-rs"

    for i in range(vm_sts["spec"]["replicas"]):
        process_name = f"{sts_name}-{i}"
        hostname = f"{process_name}.{svc_name}.{namespace}.svc.cluster.local"

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

        processes.append(
            {
                "version": mdb_version,
                "name": process_name,
                "hostname": hostname,
                "logRotate": {"sizeThresholdMB": 1000, "timeThresholdHrs": 24},
                "authSchemaVersion": 5,
                "featureCompatibilityVersion": fcv_from_version(mdb_version),
                "processType": "mongod",
                "args2_6": {
                    "net": net,
                    "storage": {"dbPath": "/data/"},
                    "systemLog": {"path": "/data/mongodb.log", "destination": "file"},
                    "replication": {"replSetName": rs_name},
                },
            }
        )
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


@fixture(scope="module")
def generated_cr_yaml(namespace: str) -> str:
    return run_migrate_generate(namespace)


@fixture(scope="module")
def generated_cr(generated_cr_yaml: str) -> dict:
    return next(yaml.safe_load_all(generated_cr_yaml))


@fixture(scope="module")
def mdb_migration(
    namespace: str,
    generated_cr: dict,
    operator_server_certs: str,
    issuer_ca_configmap: str,
) -> MongoDB:
    resource = MongoDB(RS_NAME, namespace)
    if try_load(resource):
        return resource

    resource.backing_obj = generated_cr
    resource.backing_obj["spec"].setdefault("security", {}).setdefault("tls", {})["ca"] = issuer_ca_configmap
    resource.update()
    return resource


@fixture(scope="module")
def ac_before_migration(om_tester: OMTester) -> dict:
    return om_tester.api_get_automation_config()


@fixture(scope="module")
def ac_before_promote(om_tester: OMTester) -> dict:
    return om_tester.api_get_automation_config()


# ---------------------------------------------------------------------------
# Tests — multi-step AC setup
# ---------------------------------------------------------------------------

@mark.e2e_vm_migration_generate_x509
def test_deploy_vm(namespace: str, vm_sts, vm_service):
    def sts_is_ready():
        sts = get_statefulset(namespace, vm_sts["metadata"]["name"])
        return sts.status.ready_replicas == 3

    KubernetesTester.wait_until(sts_is_ready, timeout=300)


@mark.e2e_vm_migration_generate_x509
def test_vm_ac_no_auth(
    om_tester: OMTester, vm_sts: dict, vm_service: dict, namespace: str, custom_mdb_version: str,
):
    """Step 1: start VM RS without auth or TLS so the replica set forms and agents register."""
    ac = om_tester.api_get_automation_config()
    if len(ac["processes"]) > 0:
        return

    agent_image_tag = vm_sts["spec"]["template"]["spec"]["containers"][0]["image"].split(":")[-1]
    if "agentVersion" in ac:
        ac["agentVersion"]["name"] = agent_image_tag

    processes, monitoring_versions, members = _build_processes(
        vm_sts, vm_service, namespace, custom_mdb_version, tls=False,
    )
    rs_name = f"{vm_sts['metadata']['name']}-rs"
    ac["processes"] = processes
    ac["monitoringVersions"] = monitoring_versions
    ac["replicaSets"] = [{"_id": rs_name, "members": members, "protocolVersion": "1"}]
    om_tester.api_put_automation_config(ac)
    om_tester.wait_agents_ready(timeout=600)


@mark.e2e_vm_migration_generate_x509
def test_vm_ac_tls(
    om_tester: OMTester, vm_sts: dict, vm_service: dict, namespace: str, custom_mdb_version: str,
):
    """Step 2: enable TLS on running RS (auth still disabled).
    OM rejects simultaneous TLS + auth changes, so TLS must be a separate step.
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
    processes, monitoring_versions, _ = _build_processes(
        vm_sts, vm_service, namespace, custom_mdb_version, tls=True,
    )
    ac["processes"] = processes
    ac["monitoringVersions"] = monitoring_versions
    om_tester.api_put_automation_config(ac)
    om_tester.wait_agents_ready(timeout=600)


@mark.e2e_vm_migration_generate_x509
def test_vm_ac_x509_auth(
    om_tester: OMTester,
    vm_sts: dict,
    vm_service: dict,
    namespace: str,
    custom_mdb_version: str,
    vm_agent_combined_pem: tuple,
):
    """Step 3: enable X509 auth. Agent authenticates via X509 cert.
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


# ---------------------------------------------------------------------------
# Tests — generated CR validation
# ---------------------------------------------------------------------------

@mark.e2e_vm_migration_generate_x509
def test_log_ac_after_vm_setup(om_tester: OMTester):
    log_automation_config(om_tester.api_get_automation_config(), label="after-vm-setup")


@mark.e2e_vm_migration_generate_x509
def test_install_operator(operator: Operator):
    operator.assert_is_running()


@mark.e2e_vm_migration_generate_x509
def test_tls_enabled_in_cr(generated_cr: dict):
    """Generated CR must have spec.security.tls.enabled: true."""
    tls = generated_cr.get("spec", {}).get("security", {}).get("tls", {})
    assert tls.get("enabled") is True, f"Expected tls.enabled=true, got: {tls}"


@mark.e2e_vm_migration_generate_x509
def test_auth_modes_x509_only(generated_cr: dict):
    """Authentication modes must contain only X509 (no SCRAM)."""
    modes = generated_cr["spec"]["security"]["authentication"].get("modes", [])
    assert "X509" in modes, f"Expected X509 in modes, got: {modes}"
    assert "SCRAM-SHA-256" not in modes, f"SCRAM-SHA-256 should not be in modes for x509-only, got: {modes}"


@mark.e2e_vm_migration_generate_x509
def test_no_internal_cluster_x509(generated_cr: dict):
    """No internalCluster field — keyFile is the default and should not be set."""
    auth = generated_cr["spec"]["security"]["authentication"]
    assert auth.get("internalCluster") in (None, ""), (
        f"Expected no internalCluster (keyFile default), got: {auth.get('internalCluster')}"
    )


@mark.e2e_vm_migration_generate_x509
def test_external_members_structure(generated_cr: dict):
    ext = generated_cr["spec"]["externalMembers"]
    assert len(ext) == 3
    for em in ext:
        for key in ("processName", "hostname", "type", "replicaSetName"):
            assert key in em, f"Missing key {key!r} in externalMember: {em}"


# ---------------------------------------------------------------------------
# Tests — migration lifecycle
# ---------------------------------------------------------------------------

@mark.e2e_vm_migration_generate_x509
def test_migrate_vm_to_kubernetes(mdb_migration: MongoDB, ac_before_migration: dict):
    mdb_migration.assert_reaches_phase(Phase.Running, timeout=1200)


@mark.e2e_vm_migration_generate_x509
def test_log_ac_after_migration(om_tester: OMTester, ac_before_migration: dict):
    ac_after = om_tester.api_get_automation_config()
    log_automation_config(ac_after, label="after-migration")
    log_automation_config_diff(ac_before_migration, ac_after)


@mark.e2e_vm_migration_generate_x509
def test_promote_and_prune(mdb_migration: MongoDB, vm_sts, ac_before_promote: dict):
    try_load(mdb_migration)
    for i in range(vm_sts["spec"]["replicas"]):
        mdb_migration["spec"]["memberConfig"][i]["priority"] = "1"
        mdb_migration["spec"]["memberConfig"][i]["votes"] = 1
        mdb_migration.update()
        mdb_migration.assert_reaches_phase(Phase.Running, timeout=1200)

        mdb_migration["spec"]["externalMembers"].pop()
        mdb_migration.update()
        mdb_migration.assert_reaches_phase(Phase.Running, timeout=1200)


@mark.e2e_vm_migration_generate_x509
def test_log_ac_after_promote(om_tester: OMTester, ac_before_promote: dict):
    ac_after = om_tester.api_get_automation_config()
    log_automation_config(ac_after, label="after-promote")
    log_automation_config_diff(ac_before_promote, ac_after)


@mark.e2e_vm_migration_generate_x509
def test_log_ac_end_to_end(om_tester: OMTester, ac_before_migration: dict):
    ac_after = om_tester.api_get_automation_config()
    log_automation_config(ac_after, label="final")
    log_automation_config_diff(ac_before_migration, ac_after)
