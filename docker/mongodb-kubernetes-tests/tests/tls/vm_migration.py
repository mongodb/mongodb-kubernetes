"""VM migration E2E: pseudo-VM replica set, then MongoDB CR with externalMembers and promote/prune.

Pseudo-VM pods run the automation agent from the same image tag as AGENT_IMAGE on the operator.
Automation config sets agentVersion.name to that tag so it matches the VM agents.

"""

import yaml
from kubetester import get_statefulset, try_load
from kubetester.kubetester import KubernetesTester, fcv_from_version
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB
from kubetester.omtester import OMContext, OMTester
from kubetester.operator import Operator
from kubetester.phase import Phase
from pytest import fixture, mark
from tests.tls.vm_migration_dry_run import run_migration_dry_run_connectivity_passes


@fixture(scope="module")
def om_tester(namespace: str, operator) -> OMTester:
    config_map = KubernetesTester.read_configmap(namespace, "my-project")
    secret = KubernetesTester.read_secret(namespace, "my-credentials")
    tester = OMTester(OMContext.build_from_config_map_and_secret(config_map, secret))
    tester.ensure_agent_api_key()
    return tester


@fixture(scope="module")
def vm_sts(namespace: str, om_tester: OMTester):
    with open(yaml_fixture("vm_statefulset.yaml"), "r") as f:
        sts_body = yaml.safe_load(f.read())

        sts_body["spec"]["template"]["spec"]["containers"][0]["env"] = [
            {"name": "MMS_GROUP_ID", "value": om_tester.context.project_id},
            {"name": "MMS_BASE_URL", "value": om_tester.context.base_url},
            {"name": "MMS_API_KEY", "value": om_tester.context.agent_api_key},
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
def mdb_migration(namespace: str, custom_mdb_version: str, vm_sts, vm_service) -> MongoDB:
    resource = MongoDB.from_yaml(yaml_fixture("replica-set.yaml"), namespace=namespace)

    if try_load(resource):
        return resource

    resource.set_version(custom_mdb_version)
    resource["spec"]["replicaSetNameOverride"] = f"{vm_sts['metadata']['name']}-rs"

    resource["spec"]["externalMembers"] = []
    for i in range(vm_sts["spec"]["replicas"]):
        resource["spec"]["externalMembers"].append(
            {
                "processName": f"{vm_sts['metadata']['name']}-{i}",
                "hostname": f"{vm_sts['metadata']['name']}-{i}.{vm_service['metadata']['name']}.{namespace}.svc.cluster.local",
                "type": "mongod",
                "replicaSetName": f"{vm_sts['metadata']['name']}-rs",
            }
        )

    resource["spec"]["memberConfig"] = []
    for i in range(resource.get_members()):
        resource["spec"]["memberConfig"].append(
            {
                "votes": 0,
                "priority": "0",
            }
        )
    resource.create()
    return resource


def _connection_string(namespace: str, mdb_migration: MongoDB) -> str:
    """Read connectionString.standard from the credential-less secret published by the operator."""
    secret = KubernetesTester.read_secret(namespace, f"{mdb_migration.name}-connection-string")
    return secret.get("connectionString.standard", "")


def _k8s_hostnames(mdb_migration: MongoDB, namespace: str) -> list:
    """Return the expected k8s pod DNS hostnames (host:port) for all RS members."""
    svc = f"{mdb_migration.name}-svc"
    return [
        f"{mdb_migration.name}-{i}.{svc}.{namespace}.svc.cluster.local:27017"
        for i in range(mdb_migration.get_members())
    ]


@mark.e2e_vm_migration
def test_deploy_vm(namespace: str, vm_sts, vm_service):
    def sts_is_ready():
        sts = get_statefulset(namespace, vm_sts["metadata"]["name"])
        return sts.status.ready_replicas == 3

    KubernetesTester.wait_until(sts_is_ready, timeout=300)


@mark.e2e_vm_migration
def test_update_vm_ac(namespace: str, om_tester: OMTester, vm_sts, vm_service, custom_mdb_version):
    ac = om_tester.api_get_automation_config()

    if len(ac["processes"]) > 0:
        # If there are already processes, it means the test is retried.
        return

    ac["processes"] = []
    ac["monitoringVersions"] = []

    ac["replicaSets"] = [
        {
            "_id": f"{vm_sts['metadata']['name']}-rs",
            "members": [],
            "protocolVersion": "1",
        }
    ]

    for i in range(vm_sts["spec"]["replicas"]):
        # Set monitoring versions
        ac["monitoringVersions"].append(
            {
                "hostname": f"{vm_sts['metadata']['name']}-{i}.{vm_service['metadata']['name']}.{namespace}.svc.cluster.local",
                "logPath": "/var/log/mongodb-mms-automation/monitoring-agent.log",
                "logRotate": {"sizeThresholdMB": 1000, "timeThresholdHrs": 24},
            }
        )

        ac["processes"].append(
            {
                "version": custom_mdb_version,
                "name": f"{vm_sts['metadata']['name']}-{i}",
                "hostname": f"{vm_sts['metadata']['name']}-{i}.{vm_service['metadata']['name']}.{namespace}.svc.cluster.local",
                "logRotate": {"sizeThresholdMB": 1000, "timeThresholdHrs": 24},
                "authSchemaVersion": 5,
                "featureCompatibilityVersion": fcv_from_version(custom_mdb_version),
                "processType": "mongod",
                "args2_6": {
                    "net": {
                        "port": 27017,
                        # This needs to be set otherwise the deployment would fail. OM will reject it.
                        # Operator sends disabled if tls is not configured. "disabled" is inconsistent with "null".
                        "tls": {"mode": "disabled"},
                    },
                    "storage": {"dbPath": "/data/"},
                    "systemLog": {"path": "/data/mongodb.log", "destination": "file"},
                    "replication": {"replSetName": f"{vm_sts['metadata']['name']}-rs"},
                },
            }
        )

        ac["replicaSets"][0]["members"].append(
            {
                "_id": i + 100,  # Temporarily avoid conflicts with member IDs created by operator
                "host": f"{vm_sts['metadata']['name']}-{i}",
                "priority": 1,
                "votes": 1,
                "secondaryDelaySecs": 0,
                "hidden": False,
                "arbiterOnly": False,
            }
        )

    om_tester.api_put_automation_config(ac)
    om_tester.wait_agents_ready(timeout=600)


@mark.e2e_vm_migration
def test_migration_dry_run_connectivity_passes(mdb_migration: MongoDB):
    """Run migration dry-run: operator only validates connectivity to externalMembers, then we clear the annotation."""
    run_migration_dry_run_connectivity_passes(mdb_migration)


@mark.e2e_vm_migration
def test_install_operator(operator: Operator):
    operator.assert_is_running()


@mark.e2e_vm_migration
def test_mdb_reaches_running(namespace: str, mdb_migration: MongoDB):
    mdb_migration.assert_reaches_phase(Phase.Running, timeout=600)

    conn_str = _connection_string(namespace, mdb_migration)
    for hostname in _k8s_hostnames(mdb_migration, namespace):
        assert hostname in conn_str, f"k8s hostname {hostname!r} missing from connection string secret"
    for em in mdb_migration["spec"]["externalMembers"]:
        assert em["hostname"] in conn_str, f"external member {em['hostname']!r} missing from connection string secret"


# TODO insert sample data, assert it is still there after migration
@mark.e2e_vm_migration
def test_promote_and_prune(namespace: str, mdb_migration: MongoDB, vm_sts):
    try_load(mdb_migration)
    for i in range(vm_sts["spec"]["replicas"]):
        mdb_migration["spec"]["memberConfig"][i]["priority"] = "1"
        mdb_migration["spec"]["memberConfig"][i]["votes"] = 1
        mdb_migration.update()

        mdb_migration.assert_reaches_phase(Phase.Running)

        # After promote: all k8s pods and all remaining VM members must be present.
        conn_str = _connection_string(namespace, mdb_migration)
        for hostname in _k8s_hostnames(mdb_migration, namespace):
            assert hostname in conn_str, f"k8s hostname {hostname!r} missing after member {i} promoted"
        for em in mdb_migration["spec"]["externalMembers"]:
            assert em["hostname"] in conn_str, f"external member {em['hostname']!r} missing after member {i} promoted"

        pruned = mdb_migration["spec"]["externalMembers"].pop()
        mdb_migration.update()
        mdb_migration.assert_reaches_phase(Phase.Running)

        # After prune: removed VM hostname must be gone; surviving hosts must be present.
        conn_str = _connection_string(namespace, mdb_migration)
        assert (
            pruned["hostname"] not in conn_str
        ), f"pruned hostname {pruned['hostname']!r} still in connection string after prune {i}"
        for hostname in _k8s_hostnames(mdb_migration, namespace):
            assert hostname in conn_str, f"k8s hostname {hostname!r} missing after prune {i}"
        for em in mdb_migration["spec"]["externalMembers"]:
            assert em["hostname"] in conn_str, f"external member {em['hostname']!r} missing after prune {i}"


@mark.e2e_vm_migration
def test_connection_string_after_full_migration(namespace: str, mdb_migration: MongoDB):
    """After all externalMembers are pruned the secret must contain only the k8s pod hostnames."""
    assert not mdb_migration["spec"].get("externalMembers"), "expected all external members to be pruned by now"
    conn_str = _connection_string(namespace, mdb_migration)
    assert conn_str.startswith("mongodb://"), "connection string must use mongodb:// scheme"
    for hostname in _k8s_hostnames(mdb_migration, namespace):
        assert hostname in conn_str, f"k8s hostname {hostname!r} missing from final connection string"
