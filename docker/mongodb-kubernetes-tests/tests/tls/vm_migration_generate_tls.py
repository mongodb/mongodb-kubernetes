"""
VM migration test using kubectl-mongodb migrate generate with CA bundle.

VM agents validate Ops Manager TLS via a CA ConfigMap (no disabled
verification). Use when the environment trusts the OM server cert
(e.g. cloud-qa.mongodb.com).

Configures a different AC profile from vm_migration_generate.py:
  - SCRAM-SHA-256 auth with one app user (admin-user)
  - operationProfiling (slowOpThresholdMs)
  - wiredTiger engine config (cacheSizeGB, journalCompressor)
  - storage.directoryPerDB
  - No auditLog, no custom roles
  - FCV

Provides SCRAM user password to the migrate tool via stdin.
"""

import os
import ssl

import yaml
from kubetester import create_or_update_configmap, get_statefulset, try_load
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

VM_AGENT_OM_CA_PATH = "/etc/mongodb-mms-ca/ca.pem"
VM_OM_CA_CONFIGMAP_NAME = "vm-mongodb-om-ca"
RS_NAME = "vm-mongodb-rs"
ADMIN_USER_PASSWORD = "adminPwd789!"


def _get_ca_bundle_content() -> str:
    """Return PEM content of a CA bundle that trusts public CAs."""
    path = None
    try:
        import certifi

        path = certifi.where()
    except ImportError:
        pass
    if not path:
        paths = ssl.get_default_verify_paths()
        path = getattr(paths, "cafile", None) or getattr(paths, "openssl_cafile", None)
    if not path:
        path = "/etc/ssl/certs/ca-certificates.crt"
    if not os.path.exists(path):
        raise FileNotFoundError(f"No CA bundle found; tried certifi, ssl.get_default_verify_paths(), and {path}")
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
def vm_om_ca_configmap(namespace: str):
    """ConfigMap with CA bundle so VM agents can validate Ops Manager TLS."""
    content = _get_ca_bundle_content()
    create_or_update_configmap(namespace, VM_OM_CA_CONFIGMAP_NAME, {"ca.pem": content})
    return VM_OM_CA_CONFIGMAP_NAME


@fixture(scope="module")
def vm_sts(namespace: str, om_tester: OMTester, vm_om_ca_configmap: str):
    return deploy_vm_statefulset(
        namespace,
        om_tester,
        extra_command_args=f"-httpsCAFile={VM_AGENT_OM_CA_PATH}",
        extra_volumes=[
            {"name": "om-ca", "configMap": {"name": vm_om_ca_configmap}},
        ],
        extra_volume_mounts=[
            {"name": "om-ca", "mountPath": "/etc/mongodb-mms-ca", "readOnly": True},
        ],
    )


@fixture(scope="module")
def vm_service(namespace: str):
    return deploy_vm_service(namespace)


def _configure_ac(namespace: str, om_tester: OMTester, vm_sts: dict, vm_service: dict, mdb_version: str):
    """Set up a SCRAM-authenticated replica set with operationProfiling and wiredTiger tuning."""
    mdb_version = ensure_ent_version(mdb_version)
    ac = om_tester.api_get_automation_config()
    if len(ac["processes"]) > 0:
        return

    sts_name = vm_sts["metadata"]["name"]
    svc_name = vm_service["metadata"]["name"]
    rs_name = f"{sts_name}-rs"

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
                "user": "admin-user",
                "db": "admin",
                "roles": [
                    {"role": "userAdminAnyDatabase", "db": "admin"},
                    {"role": "dbAdminAnyDatabase", "db": "admin"},
                ],
                "mechanisms": ["SCRAM-SHA-256"],
                "scramSha256Creds": {
                    "iterationCount": 15000,
                    "salt": "lQZ8IQAim8rd8ciYu7QEI4QsDOmyLHrOnN2wvQ==",
                    "serverKey": "YVsb7Bhkuap68wdtIkrC46OU4wrNzWgOPkqUxXDXT28=",
                    "storedKey": "3EcYkRFdPTN57pDbPg5JZwJX+XWU4k4lIIvNum8kiFA=",
                },
                "authenticationRestrictions": [],
            },
        ],
        "usersDeleted": [],
        "disabled": False,
        "authoritativeSet": True,
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
                "logRotate": {
                    "sizeThresholdMB": 1000,
                    "timeThresholdHrs": 24,
                },
                "authSchemaVersion": 5,
                "featureCompatibilityVersion": fcv_from_version(mdb_version),
                "processType": "mongod",
                "args2_6": {
                    "net": {
                        "port": 27017,
                        "tls": {"mode": "disabled"},
                    },
                    "storage": {
                        "dbPath": "/data/",
                        "directoryPerDB": True,
                        "wiredTiger": {
                            "engineConfig": {
                                "cacheSizeGB": 1,
                                "journalCompressor": "snappy",
                            },
                            "collectionConfig": {
                                "blockCompressor": "zstd",
                            },
                        },
                    },
                    "systemLog": {
                        "path": "/data/mongodb.log",
                        "destination": "file",
                    },
                    "replication": {
                        "replSetName": rs_name,
                    },
                    "operationProfiling": {
                        "mode": "slowOp",
                        "slowOpThresholdMs": 200,
                    },
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
def mdb_migration(namespace: str, om_tester: OMTester) -> MongoDB:
    resource = MongoDB(RS_NAME, namespace)
    if try_load(resource):
        return resource

    output = run_migrate_generate(namespace, passwords=[ADMIN_USER_PASSWORD])

    resource.backing_obj = next(yaml.safe_load_all(output))
    resource.backing_obj.setdefault("spec", {}).setdefault("additionalMongodConfig", {}).setdefault(
        "net", {}
    ).setdefault("tls", {})["mode"] = "disabled"
    resource.update()
    return resource


@fixture(scope="module")
def ac_before_migration(om_tester: OMTester) -> dict:
    return om_tester.api_get_automation_config()


@fixture(scope="module")
def ac_before_promote(om_tester: OMTester) -> dict:
    return om_tester.api_get_automation_config()


@mark.e2e_vm_migration_generate_tls
def test_deploy_vm(namespace: str, vm_sts, vm_service):
    def sts_is_ready():
        sts = get_statefulset(namespace, vm_sts["metadata"]["name"])
        return sts.status.ready_replicas == 3

    KubernetesTester.wait_until(sts_is_ready, timeout=300)


@mark.e2e_vm_migration_generate_tls
def test_configure_ac(namespace: str, om_tester: OMTester, vm_sts, vm_service, custom_mdb_version):
    _configure_ac(namespace, om_tester, vm_sts, vm_service, custom_mdb_version)


@mark.e2e_vm_migration_generate_tls
def test_log_ac_after_vm_setup(om_tester: OMTester):
    log_automation_config(om_tester.api_get_automation_config(), label="after-vm-setup")


@mark.e2e_vm_migration_generate_tls
def test_install_operator(operator: Operator):
    operator.assert_is_running()


@mark.e2e_vm_migration_generate_tls
def test_migrate_vm_to_kubernetes(mdb_migration: MongoDB, ac_before_migration: dict):
    mdb_migration.assert_reaches_phase(Phase.Running, timeout=1200)


@mark.e2e_vm_migration_generate_tls
def test_log_ac_after_migration(om_tester: OMTester, ac_before_migration: dict):
    ac_after = om_tester.api_get_automation_config()
    log_automation_config(ac_after, label="after-migration")
    log_automation_config_diff(ac_before_migration, ac_after)


# TODO insert sample data, assert it is still there after migration
@mark.e2e_vm_migration_generate_tls
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


@mark.e2e_vm_migration_generate_tls
def test_log_ac_after_promote(om_tester: OMTester, ac_before_promote: dict):
    ac_after = om_tester.api_get_automation_config()
    log_automation_config(ac_after, label="after-promote")
    log_automation_config_diff(ac_before_promote, ac_after)


@mark.e2e_vm_migration_generate_tls
def test_log_ac_end_to_end(om_tester: OMTester, ac_before_migration: dict):
    ac_after = om_tester.api_get_automation_config()
    log_automation_config(ac_after, label="final")
    log_automation_config_diff(ac_before_migration, ac_after)
