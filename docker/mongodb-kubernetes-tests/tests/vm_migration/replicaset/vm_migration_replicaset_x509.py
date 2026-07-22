"""
VM migration from a generated MongoDB resource with TLS and X509 agent authentication.

This test configures VM members in Ops Manager, runs kubectl-mongodb migrate-to-mck,
applies the generated resources, and verifies dry-run validation, data continuity,
connection strings, process names, and the promote and prune flow.
"""

from copy import deepcopy

import yaml
from cryptography import x509 as crypto_x509
from cryptography.hazmat.backends import default_backend
from kubetester import create_or_update_configmap, create_or_update_secret, get_statefulset, read_secret
from kubetester.certs import (
    ISSUER_CA_NAME,
    create_mongodb_tls_certs,
    create_x509_agent_tls_certs,
    create_x509_user_cert,
)
from kubetester.kubetester import KubernetesTester, fcv_from_version
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB
from kubetester.mongotester import MongoDBBackgroundTester, MongoTester, build_mongodb_connection_uri, with_x509
from kubetester.omtester import OMContext, OMTester
from kubetester.phase import Phase
from pytest import fixture, mark
from tests.vm_migration.vm_migration_common_helper import (
    apply_user_crs_and_verify_ac,
    assert_ca_file_present_in_pod,
    assert_max_voting_members_validation,
    assert_migration_data_exists,
    generated_mongodb_doc,
    generated_user_docs,
    insert_migration_data,
    run_generate_cr,
)
from tests.vm_migration.vm_migration_dry_run import (
    create_wrong_ca_configmap,
    run_migration_dry_run_connectivity_passes,
    run_wrong_ca_dry_run_fails_then_passes,
)
from tests.vm_migration.vm_migration_replicaset_helper import (
    MIN_K8S_MONGOD,
    MIN_VM_MONGOD,
    apply_generated_mongodb_resource,
    assert_common_generated_cr_shape,
    assert_connection_string_after_full_migration,
    assert_connection_string_contains_current_hosts,
    assert_k8s_process_names,
    deploy_vm_statefulset,
    promote_and_prune,
)

VM_STS_NAME = "vm-mongodb"
VM_RS_NAME = "vm-mongodb-rs"
MDB_RESOURCE_NAME = "my-replica-set"
SERVER_PEM_PATH = "/mongodb-automation/server.pem"
# Non-default CA file path, intentionally different from the operator's default TLSCaMountPath,
# to exercise spec.security.tls.caFilePath through the migrate-to-mck import and operator reconcile.
CUSTOM_CA_PEM_PATH = "/etc/mongodb-custom-ca/ca.pem"

# Custom cert path used for VM agents, intentionally different from the operator's default
# AgentCertMountPath to test that the operator preserves an arbitrary existing autoPEMKeyFilePath
# and creates a matching mount on K8s pods rather than overwriting with its own hash-based path.
CUSTOM_AGENT_CERT_DIR = "/var/lib/mongodb-mms-automation/certs"
CUSTOM_AGENT_CERT_FILENAME = "agent.pem"
CUSTOM_AGENT_CERT_PATH = f"{CUSTOM_AGENT_CERT_DIR}/{CUSTOM_AGENT_CERT_FILENAME}"

WRONG_CA_NAME = "wrong-issuer-ca"


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
    return create_mongodb_tls_certs(
        ISSUER_CA_NAME, namespace, VM_STS_NAME, f"{VM_STS_NAME}-cert", MIN_VM_MONGOD, None, VM_STS_NAME
    )


@fixture(scope="module")
def vm_agent_certs(issuer: str, namespace: str) -> str:
    return create_x509_agent_tls_certs(issuer, namespace, MDB_RESOURCE_NAME)


@fixture(scope="module")
def vm_agent_combined_pem(namespace: str, vm_agent_certs: str) -> tuple:
    """Create a combined PEM secret for VM pod cert mount and extract the agent subject DN.

    Ops Manager reads a single PEM path for tls.autoPEMKeyFilePath. The operator generated
    PEM secret is not available before the MongoDB resource reconciles, so this fixture creates
    the VM-side bootstrap secret directly.

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
    agent_secret_name, _ = vm_agent_combined_pem
    return deploy_vm_statefulset(
        namespace,
        om_tester,
        extra_volumes=[
            # MongoDB certificateKeyFile requires the cert and key in one file.
            {
                "name": "mongodb-certs",
                "secret": {
                    "secretName": vm_server_combined_pem,
                    "items": [{"key": "server.pem", "path": "server.pem"}],
                },
            },
            # CA cert mounted at the same path the operator sets in tls.CAFilePath
            # (/etc/mongodb-custom-ca/ca.pem) so VM agents remain functional after operator reconcile.
            {
                "name": "ca-cert",
                "secret": {
                    "secretName": "ca-key-pair",
                    "items": [{"key": "tls.crt", "path": "ca.pem"}],
                },
            },
            # The VM path must match tls.autoPEMKeyFilePath before and after import.
            {"name": "agent-cert", "secret": {"secretName": agent_secret_name}},
        ],
        extra_volume_mounts=[
            {"name": "mongodb-certs", "mountPath": "/mongodb-automation", "readOnly": True},
            {"name": "ca-cert", "mountPath": "/etc/mongodb-custom-ca", "readOnly": True},
            {"name": "agent-cert", "mountPath": CUSTOM_AGENT_CERT_DIR, "readOnly": True},
        ],
    )


@fixture(scope="module")
def mdb_tls_certs(issuer: str, namespace: str):
    return create_mongodb_tls_certs(
        ISSUER_CA_NAME, namespace, MDB_RESOURCE_NAME, f"mdb-{MDB_RESOURCE_NAME}-cert", MIN_K8S_MONGOD
    )


@fixture(scope="module")
def mdb_migration(
    namespace: str,
    issuer_ca_filepath: str,
    generated_cr_yaml: str,
    mdb_tls_certs: str,
    vm_agent_certs: str,
) -> MongoDB:
    """MDB generated by migrate-to-mck, with test-created TLS resources wired in."""
    resource_doc = deepcopy(generated_mongodb_doc(generated_cr_yaml))
    correct_ca_name = resource_doc["spec"]["security"]["tls"]["ca"]
    create_wrong_ca_configmap(namespace, WRONG_CA_NAME)
    resource_doc["spec"]["security"]["tls"]["ca"] = WRONG_CA_NAME

    def create_referenced_x509_resources(resource_doc: dict) -> None:
        create_or_update_configmap(namespace, correct_ca_name, {"ca-pem": open(issuer_ca_filepath).read()})

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

    return apply_generated_mongodb_resource(
        namespace,
        resource_doc,
        prepare_external_resources=create_referenced_x509_resources,
    )


@fixture(scope="module")
def generated_cr_yaml(namespace: str) -> str:
    return run_generate_cr(
        namespace,
        certs_secret_prefix="mdb",
        resource_name_override=MDB_RESOURCE_NAME,
    )


@fixture(scope="module")
def generated_cr(generated_cr_yaml: str) -> dict:
    return generated_mongodb_doc(generated_cr_yaml)


@fixture(scope="module")
def x509_client_pem_path(tmp_path_factory, namespace: str, vm_agent_certs: str) -> str:
    data = read_secret(namespace, vm_agent_certs)
    pem_path = tmp_path_factory.mktemp("x509-client") / "agent.pem"
    pem_path.write_text(data["tls.crt"] + data["tls.key"])
    return str(pem_path)


@fixture(scope="module")
def vm_app_user(issuer: str, namespace: str, tmp_path_factory) -> tuple[str, str]:
    """X509 client cert for the $external application user that is migrated.

    Returns (combined_pem_path, subject_dn). The subject DN is what mongod derives from the cert, so
    it doubles as the username seeded into $external and carried through migration. We extract it from
    the issued cert rather than hardcoding it so the seeded user and the connecting client always agree.
    """
    pem_path = str(tmp_path_factory.mktemp("x509-app-user") / "app-user.pem")
    create_x509_user_cert(issuer, namespace, path=pem_path)
    cert_pem = read_secret(namespace, "mongodbuser")["tls.crt"]
    if isinstance(cert_pem, bytes):
        cert_pem = cert_pem.decode("utf-8")
    subject_dn = crypto_x509.load_pem_x509_certificate(cert_pem.encode(), default_backend()).subject.rfc4514_string()
    return pem_path, subject_dn


@fixture(scope="module")
def x509_opts(x509_client_pem_path: str, issuer_ca_filepath: str) -> list[dict]:
    return [with_x509(x509_client_pem_path, issuer_ca_filepath)]


@fixture(scope="module")
def vm_x509_tester(namespace: str, x509_client_pem_path: str, issuer_ca_filepath: str) -> MongoTester:
    connection_string = build_mongodb_connection_uri(
        mdb_resource=VM_STS_NAME,
        namespace=namespace,
        members=MIN_VM_MONGOD,
        port="27017",
        servicename=VM_STS_NAME,
    )
    return MongoTester(connection_string, use_ssl=True, ca_path=issuer_ca_filepath)


@fixture(scope="module")
def mdb_health_checker(
    mdb_migration: MongoDB, issuer_ca_filepath: str, x509_opts: list[dict]
) -> MongoDBBackgroundTester:
    return MongoDBBackgroundTester(
        mdb_migration.tester(use_ssl=True, ca_path=issuer_ca_filepath),
        health_function_params={"attempts": 1, "opts": x509_opts},
    )


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
            }  # ty: ignore[invalid-assignment]
        monitoring_versions.append(mon_entry)
        net = {"port": 27017}
        if tls:
            net["tls"] = {
                "mode": "requireTLS",
                "certificateKeyFile": SERVER_PEM_PATH,
            }  # ty: ignore[invalid-assignment]
        else:
            net["tls"] = {"mode": "disabled"}  # ty: ignore[invalid-assignment]
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


@mark.e2e_vm_migration_replicaset_x509
def test_deploy_vm(namespace: str, vm_sts, vm_service):
    def sts_is_ready():
        sts = get_statefulset(namespace, vm_sts["metadata"]["name"])
        return sts.status.ready_replicas == vm_sts["spec"]["replicas"]

    KubernetesTester.wait_until(sts_is_ready, timeout=300)


# Test flow


@mark.e2e_vm_migration_replicaset_x509
def test_vm_ac_no_auth(om_tester: OMTester, vm_sts: dict, vm_service: dict, namespace: str, custom_mdb_version: str):
    """Start the VM replica set without auth or TLS so agents can register."""
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


@mark.e2e_vm_migration_replicaset_x509
def test_vm_ac_tls(
    om_tester: OMTester,
    vm_sts: dict,
    vm_service: dict,
    namespace: str,
    custom_mdb_version: str,
):
    """Enable TLS on the running replica set while auth is still disabled.

    Ops Manager rejects simultaneous TLS and auth changes, so TLS is configured first.
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
    om_tester.wait_agents_ready(timeout=1200)


@mark.e2e_vm_migration_replicaset_x509
def test_vm_ac_x509_auth(
    om_tester: OMTester,
    vm_sts: dict,
    vm_service: dict,
    namespace: str,
    custom_mdb_version: str,
    vm_agent_combined_pem: tuple[str, str],
    vm_app_user: tuple[str, str],
):
    """Enable X509 client auth after TLS is already configured.

    The automation user must be present in usersWanted so migrate-to-mck can preserve it.
    Internal cluster auth continues to use keyFile (SCRAM-SHA-256 for __system@local).
    """
    ac = om_tester.api_get_automation_config()
    if ac.get("auth", {}).get("disabled", True) is False:
        return

    _, agent_subject_dn = vm_agent_combined_pem
    _, app_user_subject_dn = vm_app_user
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
        "usersWanted": [
            {
                "user": agent_subject_dn,
                "db": "$external",
                "roles": [{"role": "root", "db": "admin"}],
                "mechanisms": [],
                "scramSha256Creds": None,
                "scramSha1Creds": None,
                "authenticationRestrictions": [],
            },
            {
                "user": app_user_subject_dn,
                "db": "$external",
                "roles": [{"role": "readWrite", "db": "admin"}],
                "mechanisms": [],
                "scramSha256Creds": None,
                "scramSha1Creds": None,
                "authenticationRestrictions": [],
            },
        ],
        "usersDeleted": [],
    }
    om_tester.api_put_automation_config(ac)
    om_tester.wait_agents_ready(timeout=1800)


@mark.e2e_vm_migration_replicaset_x509
def test_insert_migration_data(vm_x509_tester: MongoTester, x509_opts: list[dict]):
    insert_migration_data(vm_x509_tester, opts=x509_opts)


# Generated CR checks


@mark.e2e_vm_migration_replicaset_x509
def test_common_generated_cr_shape(generated_cr_yaml: str, generated_cr: dict, vm_sts: dict, version_id: str):
    assert_common_generated_cr_shape(generated_cr_yaml, generated_cr, version_id, vm_sts["spec"]["replicas"])


@mark.e2e_vm_migration_replicaset_x509
def test_x509_agent_auth_in_cr(generated_cr: dict):
    agents = generated_cr["spec"]["security"]["authentication"]["agents"]
    assert agents["mode"] == "X509"
    assert agents["autoPEMKeyFilePath"] == CUSTOM_AGENT_CERT_PATH
    assert agents["clientCertificateSecretRef"]["name"] == f"mdb-{MDB_RESOURCE_NAME}-agent-certs"


@mark.e2e_vm_migration_replicaset_x509
def test_ca_file_path_in_cr(generated_cr: dict):
    """The generated CR must carry the non-default CA file path from the AC."""
    assert generated_cr["spec"]["security"]["tls"]["caFilePath"] == CUSTOM_CA_PEM_PATH


@mark.e2e_vm_migration_replicaset_x509
def test_user_cr_emitted(generated_cr_yaml: str, vm_app_user: tuple[str, str]):
    # The $external app user produces a MongoDBUser CR; the agent auto-user is skipped by the tool.
    _, app_user_subject_dn = vm_app_user
    user_docs = generated_user_docs(generated_cr_yaml)
    assert len(user_docs) == 1, f"Expected 1 user CR (app user; agent skipped), got {len(user_docs)}"
    assert user_docs[0]["spec"]["db"] == "$external"
    assert user_docs[0]["spec"]["username"] == app_user_subject_dn


# Lifecycle checks


@mark.e2e_vm_migration_replicaset_x509
def test_migration_dry_run_wrong_ca_fails_then_passes(namespace: str, mdb_migration: MongoDB, generated_cr: dict):
    """Dry-run with a wrong CA must fail; restoring the correct CA must make it pass."""
    run_wrong_ca_dry_run_fails_then_passes(
        namespace,
        mdb_migration,
        f"{MDB_RESOURCE_NAME}-connectivity-check",
        WRONG_CA_NAME,
        correct_ca_name=generated_cr["spec"]["security"]["tls"]["ca"],
    )


@mark.e2e_vm_migration_replicaset_x509
def test_migration_dry_run_connectivity_passes(mdb_migration: MongoDB):
    """Run migration dry-run: operator only validates connectivity to externalMembers, then we clear the annotation."""
    run_migration_dry_run_connectivity_passes(mdb_migration)


@mark.e2e_vm_migration_replicaset_x509
def test_migrate_vm_to_kubernetes(mdb_migration: MongoDB):
    mdb_migration.assert_reaches_phase(Phase.Running, timeout=1200)
    assert_connection_string_contains_current_hosts(mdb_migration)


@mark.e2e_vm_migration_replicaset_x509
def test_ca_file_mounted_at_custom_path(namespace: str, mdb_migration: MongoDB):
    """The operator mounts the CA ConfigMap at the custom caFilePath in the migrated pod."""
    assert_ca_file_present_in_pod(namespace, f"{mdb_migration.name}-0", CUSTOM_CA_PEM_PATH)


@mark.e2e_vm_migration_replicaset_x509
def test_migration_data_exists_after_migration(mdb_migration: MongoDB, issuer_ca_filepath: str, x509_opts: list[dict]):
    assert_migration_data_exists(mdb_migration.tester(use_ssl=True, ca_path=issuer_ca_filepath), opts=x509_opts)


@mark.e2e_vm_migration_replicaset_x509
def test_max_voting_members_validation(mdb_migration: MongoDB):
    assert_max_voting_members_validation(mdb_migration)


@mark.e2e_vm_migration_replicaset_x509
def test_user_crs_reach_updated(generated_cr_yaml: str, namespace: str, mdb_migration: MongoDB, om_tester: OMTester):
    apply_user_crs_and_verify_ac(generated_cr_yaml, namespace, om_tester)


@mark.e2e_vm_migration_replicaset_x509
def test_app_user_x509_connectivity_after_migration(
    mdb_migration: MongoDB, vm_app_user: tuple[str, str], issuer_ca_filepath: str
):
    """The migrated $external user can authenticate via X509 (readWrite on admin)."""
    app_user_pem, _ = vm_app_user
    mdb_migration.tester(use_ssl=True, ca_path=issuer_ca_filepath).assert_x509_authentication(
        cert_file_name=app_user_pem, tlsCAFile=issuer_ca_filepath
    )


@mark.e2e_vm_migration_replicaset_x509
def test_start_background_health_checker(mdb_health_checker: MongoDBBackgroundTester):
    mdb_health_checker.start()


@mark.e2e_vm_migration_replicaset_x509
def test_promote_and_prune(mdb_migration: MongoDB, vm_sts):
    promote_and_prune(mdb_migration, vm_sts)


@mark.e2e_vm_migration_replicaset_x509
def test_mongodb_reachable_during_promote_and_prune(mdb_health_checker: MongoDBBackgroundTester):
    mdb_health_checker.assert_healthiness()
    mdb_health_checker.stop()


@mark.e2e_vm_migration_replicaset_x509
def test_connection_string_after_full_migration(mdb_migration: MongoDB):
    assert_connection_string_after_full_migration(mdb_migration)


@mark.e2e_vm_migration_replicaset_x509
def test_process_names(om_tester: OMTester, mdb_migration: MongoDB):
    assert_k8s_process_names(om_tester, mdb_migration)


@mark.e2e_vm_migration_replicaset_x509
def test_migration_data_exists_after_promote(mdb_migration: MongoDB, issuer_ca_filepath: str, x509_opts: list[dict]):
    assert_migration_data_exists(mdb_migration.tester(use_ssl=True, ca_path=issuer_ca_filepath), opts=x509_opts)
