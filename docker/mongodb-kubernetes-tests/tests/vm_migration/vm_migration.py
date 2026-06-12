"""VM migration E2E: pseudo-VM replica set, then MongoDB CR with externalMembers and promote/prune.

Starts with ``spec.members: 0`` and VM-only ``externalMembers`` (K8s StatefulSet scale 0), then scales
K8s members up while pruning VM members.

Pseudo-VM pods run the automation agent from the same image tag as AGENT_IMAGE on the operator.
Automation config sets agentVersion.name to that tag so it matches the VM agents.

"""

from typing import Tuple

import yaml
from kubetester import get_statefulset, try_load
from kubetester.kubetester import KubernetesTester, fcv_from_version
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.kubetester import skip_if_local
from kubetester.mongodb import MongoDB
from kubetester.mongotester import MongoDBBackgroundTester, MongoTester
from kubetester.omtester import OMContext, OMTester
from kubetester.phase import Phase
from pytest import fixture, mark
from tests.common.mongodb_tools_pod import mongodb_tools_pod
from tests.common.search import movies_search_helper
from tests.common.search.search_tester import SearchTester
from tests.tls.vm_migration_dry_run import run_migration_dry_run_connectivity_passes
from tests.tls.vm_migration_promote_prune import _connection_string, _k8s_hostnames, promote_and_prune_members


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
def mdb_k8s(namespace: str, custom_mdb_version: str, vm_sts, vm_service) -> MongoDB:
    resource = MongoDB.from_yaml(yaml_fixture("replica-set.yaml"), namespace=namespace)

    if try_load(resource):
        return resource

    resource.set_version(custom_mdb_version)
    resource["spec"]["replicaSetNameOverride"] = f"{vm_sts['metadata']['name']}-rs"
    # No in-cluster mongods yet; replica set is VM-only until promote_and_prune scales members up.
    resource["spec"]["members"] = 0
    resource["spec"]["externalMembers"] = []
    for i in range(vm_sts["spec"]["replicas"]):
        resource["spec"]["externalMembers"].append(
            {
                "processName": f"{vm_sts['metadata']['name']}-{i}",
                "hostname": f"{vm_sts['metadata']['name']}-{i}.{vm_service['metadata']['name']}.{namespace}.svc.cluster.local:27017",
                "type": "mongod",
                "replicaSetName": f"{vm_sts['metadata']['name']}-rs",
            }
        )

    resource["spec"]["memberConfig"] = []
    resource.create()
    return resource


@fixture(scope="module")
def mongo_tester(mdb_k8s: MongoDB):
    return mdb_k8s.tester()


@mark.e2e_vm_migration
def test_deploy_vm(namespace: str, vm_sts, vm_service):
    def sts_is_ready():
        sts = get_statefulset(namespace, vm_sts["metadata"]["name"])
        return sts.status.ready_replicas == vm_sts["spec"]["replicas"]

    KubernetesTester.wait_until(sts_is_ready, timeout=300)


@mark.e2e_vm_migration
def test_update_vm_ac(namespace: str, om_tester: OMTester, vm_sts, vm_service, custom_mdb_version):
    ac = om_tester.api_get_automation_config()

    if len(ac["processes"]) > 0:
        # If there are already processes, it means the test is retried.
        return

    ac["processes"] = []
    ac["monitoringVersions"] = []
    ac["backupVersions"] = []

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

        ac["backupVersions"].append(
            {
                "hostname": f"{vm_sts['metadata']['name']}-{i}.{vm_service['metadata']['name']}.{namespace}.svc.cluster.local",
                "logPath": "/var/log/mongodb-mms-automation/backup-agent.log",
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
                "_id": i,  # This id should not conflict with the operator generated ids.
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
def test_vm_deployment_automation_config(om_tester: OMTester, vm_sts):
    ac_tester = om_tester.get_automation_config_tester()

    assert len(ac_tester.get_all_processes()) == vm_sts["spec"]["replicas"]
    assert len(ac_tester.get_monitoring_versions()) == vm_sts["spec"]["replicas"]
    assert len(ac_tester.get_backup_versions()) == vm_sts["spec"]["replicas"]
    assert len(ac_tester.get_replica_set_processes(f"{vm_sts['metadata']['name']}-rs")) == vm_sts["spec"]["replicas"]


@mark.e2e_vm_migration
def test_migration_dry_run_connectivity_passes(mdb_k8s: MongoDB):
    """Run migration dry-run: operator only validates connectivity to externalMembers, then we clear the annotation."""
    run_migration_dry_run_connectivity_passes(mdb_k8s)


@mark.e2e_vm_migration
def test_insert_sample_data(om_tester: OMTester, vm_sts, namespace):
    sample_movies_helper = movies_search_helper.SampleMoviesSearchHelper(
        SearchTester(
            f"mongodb://{vm_sts['metadata']['name']}-0.{vm_sts['metadata']['name']}.{namespace}.svc.cluster.local:27017/?replicaSet={vm_sts['metadata']['name']}-rs"
        ),
        mongodb_tools_pod.get_tools_pod(namespace),
    )
    sample_movies_helper.restore_sample_database()


@mark.e2e_vm_migration
def test_mdb_reaches_running(namespace: str, mdb_k8s: MongoDB, om_tester: OMTester, vm_sts):
    mdb_k8s.assert_reaches_phase(Phase.Running, timeout=600)

    conn_str, _ = _connection_string(mdb_k8s)
    for hostname in _k8s_hostnames(mdb_k8s):
        assert hostname in conn_str, f"k8s hostname {hostname!r} missing from connection string secret"
    for em in mdb_k8s["spec"]["externalMembers"]:
        assert em["hostname"] in conn_str, f"external member {em['hostname']!r} missing from connection string secret"

    total_members = len(mdb_k8s["spec"]["externalMembers"]) + mdb_k8s.get_members()
    ac_tester = om_tester.get_automation_config_tester()
    assert len(ac_tester.get_all_processes()) == total_members
    assert len(ac_tester.get_monitoring_versions()) == total_members
    assert len(ac_tester.get_backup_versions()) == total_members
    assert len(ac_tester.get_replica_set_processes(f"{vm_sts['metadata']['name']}-rs")) == total_members


@mark.e2e_vm_migration
def test_max_voting_members_validation(mdb_k8s: MongoDB):
    # Update all members as voting at once, this results in 8 voting members (3 external + 5 k8s) which is more than 7
    mdb_k8s["spec"]["members"] = 5
    mdb_k8s.update()
    mdb_k8s.assert_reaches_phase(Phase.Failed, timeout=300)

    err_msg = mdb_k8s.get_status_message()
    rs_name = mdb_k8s["spec"]["replicaSetNameOverride"]
    expected_err_msg = (
        f'"{rs_name}": this reconcile would result in 8 voting members (max: 7).\n'
        "Currently voting in the Automation Config (3):\n"
        "  1. vm-mongodb-0 (external)\n"
        "  2. vm-mongodb-1 (external)\n"
        "  3. vm-mongodb-2 (external)\n"
        "This reconcile would make the following Kubernetes member(s) voting:\n"
        "  - spec.memberConfig[0]\n"
        "  - spec.memberConfig[1]\n"
        "  - spec.memberConfig[2]\n"
        "  - spec.memberConfig[3]\n"
        "  - spec.memberConfig[4]\n"
        'To fix: revert 1 of the above memberConfig entries to votes=0 and priority="0".\n'
        "If you wish to make more of the kubernetes members voting, make sure to remove one of the voting external members in the list above."
    )
    assert err_msg == expected_err_msg

    # Reset to working state
    mdb_k8s["spec"]["members"] = 0
    mdb_k8s.update()
    mdb_k8s.assert_reaches_phase(Phase.Running, timeout=300)


@mark.e2e_vm_migration
def test_promote_and_prune(mdb_k8s: MongoDB, vm_sts, om_tester: OMTester):
    promote_and_prune_members(mdb_k8s, vm_sts, om_tester, test_connection=True)


@mark.e2e_vm_migration
def test_process_names(om_tester: OMTester, mdb_k8s):
    ac_tester = om_tester.get_automation_config_tester()
    process_names = [process["name"] for process in ac_tester.get_all_processes()]
    assert f"k8s/{mdb_k8s.namespace}/{mdb_k8s.name}-0" in process_names
    assert f"k8s/{mdb_k8s.namespace}/{mdb_k8s.name}-1" in process_names
    assert f"k8s/{mdb_k8s.namespace}/{mdb_k8s.name}-2" in process_names


@mark.e2e_vm_migration
def test_connection_string_after_full_migration(mdb_k8s: MongoDB):
    """After all externalMembers are pruned the secret must contain only the k8s pod hostnames."""
    assert not mdb_k8s["spec"].get("externalMembers"), "expected all external members to be pruned by now"
    conn_str, conn_srv = _connection_string(mdb_k8s)
    assert conn_str.startswith("mongodb://"), "connection string must use mongodb:// scheme"
    for hostname in _k8s_hostnames(mdb_k8s):
        assert hostname in conn_str, f"k8s hostname {hostname!r} missing from final connection string"
    assert f"replicaSet={mdb_k8s["spec"]["replicaSetNameOverride"]}" in conn_str

    assert conn_srv.startswith("mongodb+srv://"), "SRV connection string must use mongodb+srv:// scheme"
    assert f"{mdb_k8s.get_service()}.{mdb_k8s.namespace}.svc.cluster.local" in conn_srv
    assert f"replicaSet={mdb_k8s["spec"]["replicaSetNameOverride"]}" in conn_str


@mark.e2e_vm_migration
def test_sample_mflix_database_exists_and_not_empty(mongo_tester: MongoTester):
    assert "sample_mflix" in mongo_tester.client.list_database_names(), "sample_mflix database does not exist"
    assert (
        mongo_tester.client["sample_mflix"]["movies"].count_documents({}) > 0
    ), "sample_mflix.movies collection is empty"
