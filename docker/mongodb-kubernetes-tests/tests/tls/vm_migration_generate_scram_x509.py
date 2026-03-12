"""
VM migration test with SCRAM + X.509 dual auth and X509 internal cluster authentication.

Configures mongod processes with:
  - net.tls.mode: requireSSL
  - security.clusterAuthMode: x509
  - SCRAM-SHA-256 + MONGODB-X509 deployment auth mechanisms
  - An X.509 client user (CN=x509-client,O=MongoDB) in $external
  - Agent authenticates via SCRAM-SHA-256

Verifies:
  - The generated CR has spec.security.tls.enabled: true
  - spec.security.authentication.internalCluster: X509
  - spec.security.authentication.modes includes both SCRAM-SHA-256 and X509
  - X.509 user is emitted as a MongoDBUser CR
  - Full promote-and-prune lifecycle
"""

import yaml
from kubetester import create_or_update_secret, get_statefulset, read_secret, try_load
from kubetester.certs import ISSUER_CA_NAME, create_mongodb_tls_certs, create_x509_mongodb_tls_certs
from kubetester.kubetester import KubernetesTester, ensure_ent_version, fcv_from_version
from kubetester.mongodb import MongoDB
from kubetester.omtester import OMContext, OMTester
from kubetester.operator import Operator
from kubetester.phase import Phase
from pytest import fixture, mark
from tests.tls.vm_migration_helpers import (
    deploy_vm_service,
    deploy_vm_statefulset,
    log_automation_config,
    log_automation_config_diff,
    run_migrate_generate,
)

RS_NAME = "vm-mongodb-rs"
VM_STS_NAME = "vm-mongodb"
VM_SVC_NAME = "vm-mongodb"
VM_CERT_SECRET = "vm-mongodb-cert"
VM_TLS_PEM_SECRET = "vm-mongodb-tls-pem"
OPERATOR_CERT_SECRET = f"{RS_NAME}-cert"
OPERATOR_CLUSTERFILE_SECRET = f"{RS_NAME}-clusterfile"
TLS_CERT_MOUNT = "/etc/mongodb/certs"
SCRAM_USER_PASSWORD = "x509ScramPwd456!"
X509_CLIENT_SUBJECT = "CN=x509-client,O=MongoDB"


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
    """Combine tls.crt + tls.key into a single PEM for mongod certificateKeyFile."""
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
def operator_clusterfile_certs(issuer: str, namespace: str):
    """Create X509 clusterfile certs needed for internalCluster: X509."""
    return create_x509_mongodb_tls_certs(
        ISSUER_CA_NAME,
        namespace,
        RS_NAME,
        OPERATOR_CLUSTERFILE_SECRET,
        replicas=3,
    )


@fixture(scope="module")
def vm_sts(namespace: str, om_tester: OMTester, vm_tls_pem_secret: str):
    """Deploy VM StatefulSet with TLS cert volumes mounted."""
    return deploy_vm_statefulset(
        namespace,
        om_tester,
        extra_volumes=[
            {"name": "mongodb-certs", "secret": {"secretName": vm_tls_pem_secret}},
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
        ],
    )


@fixture(scope="module")
def vm_service(namespace: str):
    return deploy_vm_service(namespace)


def _configure_ac_with_x509(namespace: str, om_tester: OMTester, vm_sts: dict, vm_service: dict, mdb_version: str):
    """Set up a replica set with requireSSL, x509 clusterAuthMode, and MONGODB-X509 auth."""
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
                "user": "scram-user",
                "db": "admin",
                "roles": [{"role": "readWrite", "db": "myapp"}],
                "mechanisms": ["SCRAM-SHA-256"],
                "scramSha256Creds": {
                    "iterationCount": 15000,
                    "salt": "nretBQptExQAlKONS4Ztx8YyFfmbt+1rY9/pDw==",
                    "serverKey": "APta75PsAGG849bRvek7GqQ6v/kI29vMwcJ68CdbGV4=",
                    "storedKey": "JsW8AZHs4sI2yzK9D95hLYRFB22D60i2Hjkml2vi1Hs=",
                },
                "authenticationRestrictions": [],
            },
            {
                "user": X509_CLIENT_SUBJECT,
                "db": "$external",
                "roles": [{"role": "readWrite", "db": "secure-db"}],
                "mechanisms": [],
                "scramSha256Creds": None,
                "scramSha1Creds": None,
                "authenticationRestrictions": [],
            },
        ],
        "usersDeleted": [],
        "disabled": False,
        "authoritativeSet": False,
        "deploymentAuthMechanisms": ["SCRAM-SHA-256", "MONGODB-X509"],
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
                    "security": {
                        "clusterAuthMode": "x509",
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
                        "authenticationMechanisms": "SCRAM-SHA-256,MONGODB-X509",
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
    return run_migrate_generate(namespace, passwords=[SCRAM_USER_PASSWORD])


@fixture(scope="module")
def generated_cr(generated_cr_yaml: str) -> dict:
    return next(yaml.safe_load_all(generated_cr_yaml))


@fixture(scope="module")
def mdb_migration(
    namespace: str,
    generated_cr: dict,
    operator_server_certs: str,
    operator_clusterfile_certs: str,
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
# Tests
# ---------------------------------------------------------------------------


@mark.e2e_vm_migration_generate_scram_x509
def test_deploy_vm(namespace: str, vm_sts, vm_service):
    def sts_is_ready():
        sts = get_statefulset(namespace, vm_sts["metadata"]["name"])
        return sts.status.ready_replicas == 3

    KubernetesTester.wait_until(sts_is_ready, timeout=300)


@mark.e2e_vm_migration_generate_scram_x509
def test_configure_ac(namespace: str, om_tester: OMTester, vm_sts, vm_service, custom_mdb_version):
    _configure_ac_with_x509(namespace, om_tester, vm_sts, vm_service, custom_mdb_version)


@mark.e2e_vm_migration_generate_scram_x509
def test_log_ac_after_vm_setup(om_tester: OMTester):
    log_automation_config(om_tester.api_get_automation_config(), label="after-vm-setup")


@mark.e2e_vm_migration_generate_scram_x509
def test_install_operator(operator: Operator):
    operator.assert_is_running()


@mark.e2e_vm_migration_generate_scram_x509
def test_tls_enabled_in_cr(generated_cr: dict):
    """The generated CR must have spec.security.tls.enabled: true."""
    tls = generated_cr.get("spec", {}).get("security", {}).get("tls", {})
    assert tls.get("enabled") is True, f"Expected tls.enabled=true, got: {tls}"


@mark.e2e_vm_migration_generate_scram_x509
def test_internal_cluster_x509(generated_cr: dict):
    """spec.security.authentication.internalCluster must be X509."""
    auth = generated_cr["spec"]["security"]["authentication"]
    assert auth.get("internalCluster") == "X509", f"Expected internalCluster=X509, got: {auth.get('internalCluster')}"


@mark.e2e_vm_migration_generate_scram_x509
def test_auth_modes_include_x509(generated_cr: dict):
    """Authentication modes must include both SCRAM-SHA-256 and X509."""
    modes = generated_cr["spec"]["security"]["authentication"].get("modes", [])
    assert "SCRAM-SHA-256" in modes, f"Expected SCRAM-SHA-256 in modes, got: {modes}"
    assert "X509" in modes, f"Expected X509 in modes, got: {modes}"


@mark.e2e_vm_migration_generate_scram_x509
def test_x509_user_cr_emitted(generated_cr_yaml: str):
    """An X.509 MongoDBUser CR should be emitted for the x509 client."""
    docs = list(yaml.safe_load_all(generated_cr_yaml))
    user_docs = [d for d in docs if d and d.get("kind") == "MongoDBUser"]
    x509_users = [u for u in user_docs if u["spec"].get("db") == "$external"]
    assert len(x509_users) >= 1, f"Expected at least one $external user, got: {user_docs}"
    assert x509_users[0]["spec"]["username"] == X509_CLIENT_SUBJECT


@mark.e2e_vm_migration_generate_scram_x509
def test_scram_user_cr_emitted(generated_cr_yaml: str):
    """A SCRAM MongoDBUser CR should be emitted for the scram user."""
    docs = list(yaml.safe_load_all(generated_cr_yaml))
    user_docs = [d for d in docs if d and d.get("kind") == "MongoDBUser"]
    scram_users = [u for u in user_docs if u["spec"].get("username") == "scram-user"]
    assert len(scram_users) == 1, f"Expected one scram-user, got: {user_docs}"


@mark.e2e_vm_migration_generate_scram_x509
def test_no_disabled_tls_mode_in_additional_config(generated_cr: dict):
    """additionalMongodConfig must NOT contain net.tls.mode: disabled."""
    amc = generated_cr["spec"].get("additionalMongodConfig", {})
    tls_mode = amc.get("net", {}).get("tls", {}).get("mode")
    assert (
        tls_mode != "disabled"
    ), f"TLS is requireSSL — additionalMongodConfig should not have mode=disabled, got: {amc}"


@mark.e2e_vm_migration_generate_scram_x509
def test_external_members_structure(generated_cr: dict):
    ext = generated_cr["spec"]["externalMembers"]
    assert len(ext) == 3
    for em in ext:
        for key in ("processName", "hostname", "type", "replicaSetName"):
            assert key in em, f"Missing key {key!r} in externalMember: {em}"


@mark.e2e_vm_migration_generate_scram_x509
def test_migrate_vm_to_kubernetes(mdb_migration: MongoDB, ac_before_migration: dict):
    mdb_migration.assert_reaches_phase(Phase.Running, timeout=1200)


@mark.e2e_vm_migration_generate_scram_x509
def test_log_ac_after_migration(om_tester: OMTester, ac_before_migration: dict):
    ac_after = om_tester.api_get_automation_config()
    log_automation_config(ac_after, label="after-migration")
    log_automation_config_diff(ac_before_migration, ac_after)


@mark.e2e_vm_migration_generate_scram_x509
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


@mark.e2e_vm_migration_generate_scram_x509
def test_log_ac_after_promote(om_tester: OMTester, ac_before_promote: dict):
    ac_after = om_tester.api_get_automation_config()
    log_automation_config(ac_after, label="after-promote")
    log_automation_config_diff(ac_before_promote, ac_after)


@mark.e2e_vm_migration_generate_scram_x509
def test_log_ac_end_to_end(om_tester: OMTester, ac_before_migration: dict):
    ac_after = om_tester.api_get_automation_config()
    log_automation_config(ac_after, label="final")
    log_automation_config_diff(ac_before_migration, ac_after)
