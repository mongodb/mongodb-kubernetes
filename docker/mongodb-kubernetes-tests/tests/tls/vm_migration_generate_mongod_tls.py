"""
VM migration test with MongoDB-level TLS (requireSSL) on mongod processes,
combined with agent to Ops Manager TLS validation via a CA bundle.

Verifies:
  - The generated CR has spec.security.certsSecretPrefix set (TLS enabled; tls.enabled is deprecated)
  - No manual net.tls.mode override is needed (the tool handles it)
  - SCRAM auth and users are generated alongside TLS
  - VM agents can validate Ops Manager TLS via a CA ConfigMap
  - Full promote-and-prune lifecycle with TLS-enabled deployment
"""

import os
import ssl

import yaml
from kubetester import create_or_update_configmap, create_or_update_secret, get_statefulset, read_secret, try_load
from kubetester.certs import ISSUER_CA_NAME, create_mongodb_tls_certs
from kubetester.kubetester import KubernetesTester, ensure_ent_version, fcv_from_version, skip_if_local
from kubetester.mongodb import MongoDB
from kubetester.omtester import OMContext, OMTester
from kubetester.operator import Operator
from kubetester.phase import Phase
from pytest import fixture, mark
from tests.tls.vm_migration_dry_run import run_migration_dry_run_connectivity_passes
from tests.tls.vm_migration_helpers import (
    apply_user_crs_and_verify_ac,
    assert_migration_dry_run_annotation,
    deploy_vm_service,
    deploy_vm_statefulset,
    promote_and_prune,
    rotate_password_and_verify,
    run_generate_cr,
    vm_replica_set_tester,
)

RS_NAME = "vm-mongodb-rs"
VM_STS_NAME = "vm-mongodb"
VM_SVC_NAME = "vm-mongodb"
VM_CERT_SECRET = "vm-mongodb-cert"
VM_TLS_PEM_SECRET = "vm-mongodb-tls-pem"
# Match migration tool output: certsSecretPrefix: mdb -> secret name mdb-<resource-name>-cert
CERT_SECRET_PREFIX = "mdb"
OPERATOR_CERT_SECRET = f"{CERT_SECRET_PREFIX}-{RS_NAME}-cert"
TLS_CERT_MOUNT = "/etc/mongodb/certs"
APP_USER_PASSWORD = "tlsAppUser123!"
VM_AGENT_OM_CA_PATH = "/etc/mongodb-mms-ca/ca.pem"
VM_OM_CA_CONFIGMAP_NAME = "vm-mongodb-om-ca"


def _get_ca_bundle_content() -> str:
    """Return PEM content of the system CA bundle."""

    paths = ssl.get_default_verify_paths()
    path = paths.cafile or paths.openssl_cafile or "/etc/ssl/certs/ca-certificates.crt"
    if not os.path.exists(path):
        raise FileNotFoundError(f"No system CA bundle found at {path}; set SSL_CERT_FILE or install ca-certificates")
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
def vm_server_certs(issuer: str, namespace: str):
    """Create TLS certs for the VM mongod pods via cert-manager."""
    return create_mongodb_tls_certs(
        ISSUER_CA_NAME,
        namespace,
        VM_STS_NAME,
        VM_CERT_SECRET,
        replicas=3,
        service_name=VM_SVC_NAME,
    )


@fixture(scope="module")
def vm_tls_pem_secret(namespace: str, vm_server_certs: str):
    """Combine tls.crt + tls.key from the cert-manager secret into a single
    PEM file that mongod can use as certificateKeyFile."""
    data = read_secret(namespace, vm_server_certs)
    server_pem = data["tls.crt"] + data["tls.key"]
    ca_pem = data["ca.crt"]
    create_or_update_secret(
        namespace,
        VM_TLS_PEM_SECRET,
        {"server.pem": server_pem, "ca.pem": ca_pem},
    )
    return VM_TLS_PEM_SECRET


@fixture(scope="module")
def operator_server_certs(issuer: str, namespace: str):
    """Create TLS certs for the operator-managed pods (post-migration)."""
    return create_mongodb_tls_certs(
        ISSUER_CA_NAME,
        namespace,
        RS_NAME,
        OPERATOR_CERT_SECRET,
        replicas=3,
    )


@fixture(scope="module")
def vm_om_ca_configmap(namespace: str):
    """ConfigMap with CA bundle so VM agents can validate Ops Manager TLS."""
    content = _get_ca_bundle_content()
    create_or_update_configmap(namespace, VM_OM_CA_CONFIGMAP_NAME, {"ca.pem": content})
    return VM_OM_CA_CONFIGMAP_NAME


@fixture(scope="module")
def vm_sts(namespace: str, om_tester: OMTester, vm_tls_pem_secret: str, vm_om_ca_configmap: str):
    """Deploy VM StatefulSet with mongod TLS cert volumes and OM CA bundle mounted."""
    return deploy_vm_statefulset(
        namespace,
        om_tester,
        extra_command_args=f"-httpsCAFile={VM_AGENT_OM_CA_PATH}",
        extra_volumes=[
            {"name": "mongodb-certs", "secret": {"secretName": vm_tls_pem_secret}},
            {"name": "om-ca", "configMap": {"name": vm_om_ca_configmap}},
        ],
        extra_volume_mounts=[
            {
                "name": "mongodb-certs",
                "mountPath": "/mongodb-automation/server.pem",
                "subPath": "server.pem",
                "readOnly": True,
            },
            {
                "name": "mongodb-certs",
                "mountPath": "/mongodb-automation/tls/ca/ca-pem",
                "subPath": "ca.pem",
                "readOnly": True,
            },
            {"name": "om-ca", "mountPath": "/etc/mongodb-mms-ca", "readOnly": True},
        ],
    )


@fixture(scope="module")
def vm_service(namespace: str):
    return deploy_vm_service(namespace)


def _configure_ac_with_tls(namespace: str, om_tester: OMTester, vm_sts: dict, vm_service: dict, mdb_version: str):
    """Set up a TLS-enabled replica set (requireSSL) with SCRAM auth."""
    mdb_version = ensure_ent_version(mdb_version)
    ac = om_tester.api_get_automation_config()
    if len(ac["processes"]) > 0:
        return

    sts_name = vm_sts["metadata"]["name"]
    svc_name = vm_service["metadata"]["name"]
    rs_name = f"{sts_name}-rs"

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
                "scramSha256Creds": {
                    "iterationCount": 15000,
                    "salt": "VvGtJFS/4euDEKqliOPW6idGBu4SMey5HgtRoQ==",
                    "serverKey": "xsHGbx5OJnYtZS19a4EboChhlD3mhDt7qOJss+FrShY=",
                    "storedKey": "1z/5Z7A5mlHt5lu/ZXUig5bwrBfOn3tzqTzn93Bf/Oo=",
                },
                "authenticationRestrictions": [],
            },
            {
                "user": "app-user",
                "db": "admin",
                "roles": [{"role": "readWriteAnyDatabase", "db": "admin"}],
                "mechanisms": ["SCRAM-SHA-256"],
                "scramSha256Creds": {
                    "iterationCount": 15000,
                    "salt": "Qll4OI2xpysKmK1jv03JhlYQn+P7SUKbF3kdxA==",
                    "serverKey": "V4PfLQcW/aOwfXeCvgWYfvv9cS04HTB9nUPJy9JzNqM=",
                    "storedKey": "i8dJpjHY0uyFh89TcjT7JS27N6FTY98TiRV/J+jRBDo=",
                },
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

    ac["processes"] = []
    ac["monitoringVersions"] = []
    ac["replicaSets"] = [{"_id": rs_name, "members": [], "protocolVersion": "1"}]

    for i in range(vm_sts["spec"]["replicas"]):
        hostname = f"{sts_name}-{i}.{svc_name}.{namespace}.svc.cluster.local"

        ac["monitoringVersions"].append(
            {
                "hostname": hostname,
                "logPath": "/var/log/mongodb-mms-automation/monitoring-agent.log",
                "logRotate": {"sizeThresholdMB": 1000, "timeThresholdHrs": 24},
            }
        )

        ac["processes"].append(
            {
                "version": mdb_version,
                "name": f"{sts_name}-{i}",
                "hostname": hostname,
                "logRotate": {"sizeThresholdMB": 1000, "timeThresholdHrs": 24},
                "authSchemaVersion": 5,
                "featureCompatibilityVersion": fcv_from_version(mdb_version),
                "processType": "mongod",
                "args2_6": {
                    "net": {
                        "port": 27017,
                        "tls": {
                            "mode": "requireSSL",
                            "certificateKeyFile": "/mongodb-automation/server.pem",
                            "CAFile": "/mongodb-automation/tls/ca/ca-pem",
                        },
                    },
                    "storage": {
                        "dbPath": "/data/",
                        "directoryPerDB": True,
                    },
                    "systemLog": {
                        "path": "/data/mongodb.log",
                        "destination": "file",
                    },
                    "replication": {"replSetName": rs_name},
                    "setParameter": {
                        "authenticationMechanisms": "SCRAM-SHA-256",
                    },
                },
            }
        )

        ac["replicaSets"][0]["members"].append(
            {
                "_id": i + 100,
                "host": f"{sts_name}-{i}",
                "priority": 1,
                "votes": 1,
                "secondaryDelaySecs": 0,
                "hidden": False,
                "arbiterOnly": False,
            }
        )

    om_tester.api_put_automation_config(ac)


@fixture(scope="module")
def generated_cr_yaml(namespace: str) -> str:
    create_or_update_secret(namespace, "app-user-secret", {"password": APP_USER_PASSWORD})
    return run_generate_cr(
        namespace,
        user_secrets={"app-user:admin": "app-user-secret"},
        certs_secret_prefix=CERT_SECRET_PREFIX,
    )


@fixture(scope="module")
def generated_cr(generated_cr_yaml: str) -> dict:
    return next(yaml.safe_load_all(generated_cr_yaml))


@fixture(scope="module")
def migrate_tool_ca_configmap(namespace: str, issuer_ca_filepath: str, generated_cr: dict) -> str:
    """Create the CA ConfigMap with the name the migrate tool outputs in security.tls.ca.

    The generated CR sets security.tls.ca to '<resourceName>-ca' by default.
    This fixture creates a ConfigMap with that exact name so the link between
    the tool output and the actual resource is explicit.
    """
    ca_name = generated_cr["spec"]["security"]["tls"]["ca"]
    ca_pem = open(issuer_ca_filepath).read()
    create_or_update_configmap(namespace, ca_name, {"ca-pem": ca_pem})
    return ca_name


@fixture(scope="module")
def mdb_migration(
    namespace: str,
    generated_cr: dict,
    operator_server_certs: str,
    migrate_tool_ca_configmap: str,
) -> MongoDB:
    resource = MongoDB(RS_NAME, namespace)
    if try_load(resource):
        return resource

    resource.backing_obj = generated_cr
    # The generated CR already has security.tls.ca set to the migrate_tool_ca_configmap name.
    # No override needed, the link between the generated CR and the ConfigMap is explicit.
    # The generated CR starts with members=0 and no memberConfig.
    # Set members to match the VM replica count and add draining memberConfig.
    num_members = len(generated_cr["spec"].get("externalMembers", []))
    resource.backing_obj["spec"]["members"] = num_members
    resource.backing_obj["spec"]["memberConfig"] = [{"votes": 0, "priority": "0"} for _ in range(num_members)]
    resource.update()
    return resource


# ---------------------------------------------------------------------------
# Tests
# ---------------------------------------------------------------------------


@mark.e2e_vm_migration_generate_mongod_tls
def test_deploy_vm(namespace: str, vm_sts, vm_service):
    def sts_is_ready():
        sts = get_statefulset(namespace, vm_sts["metadata"]["name"])
        return sts.status.ready_replicas == 3

    KubernetesTester.wait_until(sts_is_ready, timeout=300)


@mark.e2e_vm_migration_generate_mongod_tls
def test_configure_ac(namespace: str, om_tester: OMTester, vm_sts, vm_service, custom_mdb_version):
    _configure_ac_with_tls(namespace, om_tester, vm_sts, vm_service, custom_mdb_version)


@mark.e2e_vm_migration_generate_mongod_tls
@skip_if_local()
def test_user_connectivity_before_migration(namespace: str, ca_path: str):
    """Users can authenticate against the VM replica set (with TLS) before migration."""
    vm_replica_set_tester(namespace, use_ssl=True, ca_path=ca_path).assert_scram_sha_authentication(
        username="app-user", password=APP_USER_PASSWORD, auth_mechanism="SCRAM-SHA-256", ssl=True, tlsCAFile=ca_path
    )


@mark.e2e_vm_migration_generate_mongod_tls
@skip_if_local()
def test_non_tls_connection_rejected_before_migration(namespace: str):
    """TLS is enforced on the VM replica set -- plain connections must be rejected."""
    vm_replica_set_tester(namespace, use_ssl=False).assert_no_connection()


@mark.e2e_vm_migration_generate_mongod_tls
def test_install_operator(operator: Operator):
    operator.assert_is_running()


# --- Generated CR checks (all run immediately after generation, before any lifecycle test) ---


@mark.e2e_vm_migration_generate_mongod_tls
def test_migration_dry_run_annotation_present(generated_cr_yaml: str):
    """Generated MongoDB CR must carry the migration-dry-run annotation."""
    assert_migration_dry_run_annotation(generated_cr_yaml)


@mark.e2e_vm_migration_generate_mongod_tls
def test_tls_enabled_in_cr(generated_cr: dict):
    """The generated CR must have TLS enabled via spec.security.certsSecretPrefix (tls.enabled is deprecated)."""
    security = generated_cr.get("spec", {}).get("security", {})
    prefix = security.get("certsSecretPrefix")
    assert prefix, f"Expected certsSecretPrefix to be set for TLS, got security: {security}"


@mark.e2e_vm_migration_generate_mongod_tls
def test_no_disabled_tls_mode_in_additional_config(generated_cr: dict):
    """additionalMongodConfig must NOT contain net.tls.mode: disabled."""
    amc = generated_cr["spec"].get("additionalMongodConfig", {})
    tls_mode = amc.get("net", {}).get("tls", {}).get("mode")
    assert (
        tls_mode != "disabled"
    ), f"TLS is requireSSL -- additionalMongodConfig should not have mode=disabled, got: {amc}"


@mark.e2e_vm_migration_generate_mongod_tls
def test_security_auth_present(generated_cr: dict):
    """SCRAM auth must be present alongside TLS."""
    auth = generated_cr["spec"]["security"].get("authentication", {})
    assert auth.get("enabled") is True
    assert "SCRAM" in auth.get("modes", [])


@mark.e2e_vm_migration_generate_mongod_tls
def test_user_cr_emitted(generated_cr_yaml: str):
    docs = list(yaml.safe_load_all(generated_cr_yaml))
    user_docs = [d for d in docs if d and d.get("kind") == "MongoDBUser"]
    assert len(user_docs) == 1, f"Expected 1 user CR, got {len(user_docs)}"


@mark.e2e_vm_migration_generate_mongod_tls
def test_external_members_structure(generated_cr: dict):
    ext = generated_cr["spec"]["externalMembers"]
    assert len(ext) == 3
    for em in ext:
        for key in ("processName", "hostname", "type", "replicaSetName"):
            assert key in em, f"Missing key {key!r} in externalMember: {em}"


# --- Lifecycle tests ---


@mark.e2e_vm_migration_generate_mongod_tls
def test_migration_dry_run_connectivity_passes(mdb_migration: MongoDB):
    """Operator validates connectivity to all externalMembers, then the annotation is removed."""
    run_migration_dry_run_connectivity_passes(mdb_migration)


@mark.e2e_vm_migration_generate_mongod_tls
def test_migrate_vm_to_kubernetes(mdb_migration: MongoDB):
    mdb_migration.assert_reaches_phase(Phase.Running, timeout=1200)


@mark.e2e_vm_migration_generate_mongod_tls
def test_user_crs_reach_updated(generated_cr_yaml: str, namespace: str, mdb_migration: MongoDB, om_tester: OMTester):
    apply_user_crs_and_verify_ac(generated_cr_yaml, namespace, om_tester)


@mark.e2e_vm_migration_generate_mongod_tls
@skip_if_local()
def test_user_connectivity_after_migration(mdb_migration: MongoDB, ca_path: str):
    """Users can still authenticate (with TLS) after the operator takes over the replica set."""
    mdb_migration.tester(use_ssl=True, ca_path=ca_path).assert_scram_sha_authentication(
        username="app-user", password=APP_USER_PASSWORD, auth_mechanism="SCRAM-SHA-256", ssl=True, tlsCAFile=ca_path
    )


@mark.e2e_vm_migration_generate_mongod_tls
@skip_if_local()
def test_non_tls_connection_rejected_after_migration(mdb_migration: MongoDB):
    """TLS remains enforced after migration -- plain connections must still be rejected."""
    mdb_migration.tester(use_ssl=False).assert_no_connection()


@mark.e2e_vm_migration_generate_mongod_tls
def test_promote_and_prune(mdb_migration: MongoDB, vm_sts):
    promote_and_prune(mdb_migration, vm_sts)


@mark.e2e_vm_migration_generate_mongod_tls
@skip_if_local()
def test_user_connectivity_after_promote(mdb_migration: MongoDB, ca_path: str):
    """Users can still authenticate (with TLS) after promote-and-prune completes."""
    mdb_migration.tester(use_ssl=True, ca_path=ca_path).assert_scram_sha_authentication(
        username="app-user", password=APP_USER_PASSWORD, auth_mechanism="SCRAM-SHA-256", ssl=True, tlsCAFile=ca_path
    )


@mark.e2e_vm_migration_generate_mongod_tls
@skip_if_local()
def test_non_tls_connection_rejected_after_promote(mdb_migration: MongoDB):
    """TLS remains enforced after promote-and-prune."""
    mdb_migration.tester(use_ssl=False).assert_no_connection()


@mark.e2e_vm_migration_generate_mongod_tls
def test_password_rotation_keeps_migrated_flag(generated_cr_yaml: str, namespace: str, om_tester: OMTester):
    rotate_password_and_verify(generated_cr_yaml, namespace, om_tester)
