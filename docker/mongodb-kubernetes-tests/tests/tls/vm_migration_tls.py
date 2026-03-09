"""
VM migration test that uses a CA bundle so VM agents validate Ops Manager TLS
instead of disabling verification. Use this when the test environment has a
trust store (e.g. certifi or system CA) that trusts the Ops Manager server cert
(e.g. cloud-qa.mongodb.com). Uses the kubectl-mongodb migrate tool to generate
the MongoDB CR.
"""
import os
import ssl
import subprocess
import yaml
from kubetester import create_or_update_configmap, get_statefulset, try_load
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB
from kubetester.omtester import OMContext, OMTester
from kubetester.operator import Operator
from kubetester.phase import Phase
from pytest import fixture, mark
from tests.tls.vm_migration_helpers import configure_vm_replica_set, log_automation_config, log_automation_config_diff

# Path where the agent will read the CA file inside the pod
VM_AGENT_OM_CA_PATH = "/etc/mongodb-mms-ca/ca.pem"
VM_OM_CA_CONFIGMAP_NAME = "vm-mongodb-om-ca"

MIGRATE_TOOL = os.getenv("KUBECTL_MONGODB_PATH", "kubectl-mongodb")
MIGRATE_FLAGS = ["--config-map-name", "my-project", "--secret-name", "my-credentials"]
RS_NAME = "vm-mongodb-rs"


def _get_ca_bundle_content() -> str:
    """Return PEM content of a CA bundle that trusts public CAs (e.g. for Ops Manager)."""
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
    with open(yaml_fixture("vm_statefulset.yaml"), "r") as f:
        sts_body = yaml.safe_load(f.read())

    sts_body["spec"]["template"]["spec"]["containers"][0]["env"] = [
        {"name": "MMS_GROUP_ID", "value": om_tester.context.project_id},
        {"name": "MMS_BASE_URL", "value": om_tester.context.base_url},
        {"name": "MMS_API_KEY", "value": om_tester.context.agent_api_key},
    ]
    # Use CA bundle to validate Ops Manager (no disabled verification).
    cmd = sts_body["spec"]["template"]["spec"]["containers"][0]["command"]
    if isinstance(cmd, list) and len(cmd) >= 3 and "-mmsApiKey=" in cmd[2]:
        cmd[2] = cmd[2] + f" -httpsCAFile={VM_AGENT_OM_CA_PATH}"

    # Mount the CA ConfigMap
    container = sts_body["spec"]["template"]["spec"]["containers"][0]
    container["volumeMounts"] = container.get("volumeMounts", []) + [
        {"name": "om-ca", "mountPath": "/etc/mongodb-mms-ca", "readOnly": True}
    ]
    sts_body["spec"]["template"]["spec"].setdefault("volumes", [])
    sts_body["spec"]["template"]["spec"]["volumes"] = sts_body["spec"]["template"]["spec"]["volumes"] + [
        {
            "name": "om-ca",
            "configMap": {"name": vm_om_ca_configmap},
        }
    ]

    KubernetesTester.create_or_update_statefulset(namespace, body=sts_body)
    return sts_body


@fixture(scope="module")
def vm_service(namespace: str):
    with open(yaml_fixture("vm_service.yaml"), "r") as f:
        service_body = yaml.safe_load(f.read())
    KubernetesTester.create_or_update_service(namespace, body=service_body)
    return service_body


@fixture(scope="module")
def mdb_migration(namespace: str, om_tester: OMTester) -> MongoDB:
    resource = MongoDB(RS_NAME, namespace)

    if try_load(resource):
        return resource

    try:
        output = subprocess.check_output(
            [MIGRATE_TOOL, "migrate", "generate", *MIGRATE_FLAGS, "--namespace", namespace],
            stderr=subprocess.PIPE,
            text=True,
        )
    except subprocess.CalledProcessError as exc:
        print(f"migrate generate failed: {exc.stderr}")
        raise exc

    resource.backing_obj = yaml.safe_load(output)
    # The migrate tool warns that net.tls.mode: "disabled" must be added
    # manually; the operator treats an absent TLS mode as requireTLS.
    resource.backing_obj.setdefault("spec", {}).setdefault("additionalMongodConfig", {}).setdefault("net", {}).setdefault("tls", {})["mode"] = "disabled"
    resource.update()
    return resource


@mark.e2e_vm_migration_tls
def test_operator_is_running(operator: Operator):
    operator.assert_is_running()


@mark.e2e_vm_migration_tls
def test_vm_agents_are_ready(namespace: str, vm_sts, vm_service):
    def sts_is_ready():
        sts = get_statefulset(namespace, vm_sts["metadata"]["name"])
        return sts.status.ready_replicas == 3

    KubernetesTester.wait_until(sts_is_ready, timeout=300)


@mark.e2e_vm_migration_tls
def test_configure_vm_replica_set_in_om(namespace: str, om_tester: OMTester, vm_sts, vm_service, custom_mdb_version):
    configure_vm_replica_set(namespace, om_tester, vm_sts, vm_service, custom_mdb_version)


@mark.e2e_vm_migration_tls
def test_log_ac_after_vm_setup(om_tester: OMTester):
    log_automation_config(om_tester.api_get_automation_config(), label="after-vm-setup")


@fixture(scope="module")
def ac_before_migration(om_tester: OMTester) -> dict:
    """Snapshot the automation config right before the operator touches it."""
    return om_tester.api_get_automation_config()


@mark.e2e_vm_migration_tls
def test_migrate_vm_to_kubernetes(mdb_migration: MongoDB, ac_before_migration: dict):
    mdb_migration.assert_reaches_phase(Phase.Running, timeout=1200)


@mark.e2e_vm_migration_tls
def test_log_ac_after_migration(om_tester: OMTester, ac_before_migration: dict):
    ac_after = om_tester.api_get_automation_config()
    log_automation_config(ac_after, label="after-migration")
    log_automation_config_diff(ac_before_migration, ac_after)


@fixture(scope="module")
def ac_before_promote(om_tester: OMTester) -> dict:
    """Snapshot the automation config right before promote/prune."""
    return om_tester.api_get_automation_config()


@mark.e2e_vm_migration_tls
def test_promote_operator_members_and_remove_vm(mdb_migration: MongoDB, vm_sts, ac_before_promote: dict):
    try_load(mdb_migration)
    for i in range(vm_sts["spec"]["replicas"]):
        mdb_migration["spec"]["memberConfig"][i]["priority"] = "1"
        mdb_migration["spec"]["memberConfig"][i]["votes"] = 1
        mdb_migration.update()
        mdb_migration.assert_reaches_phase(Phase.Running, timeout=1200)
        mdb_migration["spec"]["externalMembers"].pop()
        mdb_migration.update()
        mdb_migration.assert_reaches_phase(Phase.Running, timeout=1200)


@mark.e2e_vm_migration_tls
def test_log_ac_after_promote(om_tester: OMTester, ac_before_promote: dict):
    ac_after = om_tester.api_get_automation_config()
    log_automation_config(ac_after, label="after-promote")
    log_automation_config_diff(ac_before_promote, ac_after)


@mark.e2e_vm_migration_tls
def test_log_ac_end_to_end(om_tester: OMTester, ac_before_migration: dict):
    ac_after = om_tester.api_get_automation_config()
    log_automation_config(ac_after, label="final")
    log_automation_config_diff(ac_before_migration, ac_after)
