"""VM migration E2E: pseudo-VM replica set, then MongoDB CR with externalMembers and promote/prune.

Pseudo-VM pods run the automation agent from the same image tag as AGENT_IMAGE on the operator.
Automation config sets agentVersion.name to that tag so it matches the VM agents.

"""

import yaml
from kubetester import get_statefulset, try_load
from kubetester.kubetester import KubernetesTester, fcv_from_version
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.kubetester import skip_if_local
from kubetester.mongodb import MongoDB
from kubetester.mongotester import MongoDBBackgroundTester, MongoTester
from kubetester.omtester import OMContext, OMTester
from kubetester.operator import Operator
from kubetester.phase import Phase
from pytest import fixture, mark
from tests.common.search import movies_search_helper
from tests.common.search.search_tester import SearchTester
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

        service_body["spec"]["clusterIP"] = "None"  # This needs to be set to None for a headless service

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
                "hostname": f"{vm_sts['metadata']['name']}-{i}.{vm_service['metadata']['name']}.{namespace}.svc.cluster.local:27017",
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


@fixture(scope="module")
def mongo_tester(mdb_migration: MongoDB):
    return mdb_migration.tester()


@fixture(scope="module")
def mdb_health_checker(mongo_tester: MongoTester) -> MongoDBBackgroundTester:
    health_checker = MongoDBBackgroundTester(mongo_tester)
    health_checker.start()
    return health_checker


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
def test_migration_dry_run_connectivity_passes(mdb_migration: MongoDB):
    """Run migration dry-run: operator only validates connectivity to externalMembers, then we clear the annotation."""
    run_migration_dry_run_connectivity_passes(mdb_migration)


@mark.e2e_vm_migration
def test_vm_deployment_is_ready(om_tester: OMTester, vm_sts):
    om_tester.wait_agents_ready()
    ac_tester = om_tester.get_automation_config_tester()

    assert len(ac_tester.get_all_processes()) == 3
    assert len(ac_tester.get_monitoring_versions()) == 3
    assert len(ac_tester.get_backup_versions()) == 3
    assert len(ac_tester.get_replica_set_processes(f"{vm_sts['metadata']['name']}-rs")) == 3


@mark.e2e_vm_migration
@skip_if_local
def test_insert_sample_data(om_tester: OMTester, vm_sts, namespace):
    sample_movies_helper = movies_search_helper.SampleMoviesSearchHelper(
        SearchTester(
            f"mongodb://{vm_sts['metadata']['name']}-0.{vm_sts['metadata']['name']}.{namespace}.svc.cluster.local:27017/?replicaSet={vm_sts['metadata']['name']}-rs"
        )
    )
    sample_movies_helper.restore_sample_database()


@mark.e2e_vm_migration
def test_install_operator(default_operator: Operator):
    default_operator.assert_is_running()


@mark.e2e_vm_migration
def test_mdb_reaches_running(mdb_migration: MongoDB, om_tester: OMTester, vm_sts):
    mdb_migration.assert_reaches_phase(Phase.Running, timeout=600)

    ac_tester = om_tester.get_automation_config_tester()
    assert len(ac_tester.get_all_processes()) == 6
    assert len(ac_tester.get_monitoring_versions()) == 6
    assert len(ac_tester.get_backup_versions()) == 6
    assert len(ac_tester.get_replica_set_processes(f"{vm_sts['metadata']['name']}-rs")) == 6


@mark.e2e_vm_migration
def test_promote_and_prune(
    mdb_migration: MongoDB, vm_sts, om_tester: OMTester, mdb_health_checker: MongoDBBackgroundTester
):
    try_load(mdb_migration)
    for i in range(vm_sts["spec"]["replicas"]):
        mdb_migration["spec"]["memberConfig"][i]["priority"] = "1"
        mdb_migration["spec"]["memberConfig"][i]["votes"] = 1
        mdb_migration.update()

        mdb_migration.assert_reaches_phase(Phase.Running)

        mdb_migration["spec"]["externalMembers"].pop()
        mdb_migration.update()
        mdb_migration.assert_reaches_phase(Phase.Running)

        om_tester.assert_cluster_available(f"{vm_sts['metadata']['name']}-rs")
        ac_tester = om_tester.get_automation_config_tester()
        assert len(ac_tester.get_all_processes()) == 3 + len(mdb_migration["spec"]["externalMembers"])
        assert len(ac_tester.get_monitoring_versions()) == 3 + len(mdb_migration["spec"]["externalMembers"])
        assert len(ac_tester.get_backup_versions()) == 3 + len(mdb_migration["spec"]["externalMembers"])
        assert len(ac_tester.get_replica_set_processes(f"{vm_sts['metadata']['name']}-rs")) == 3 + len(
            mdb_migration["spec"]["externalMembers"]
        )

    # TODO: doesn't work great locally
    mdb_health_checker.assert_healthiness()


@mark.e2e_vm_migration
def test_process_names(om_tester: OMTester, namespace, mdb_migration):
    ac_tester = om_tester.get_automation_config_tester()
    process_names = [process["name"] for process in ac_tester.get_all_processes()]
    assert f"k8s/{namespace}/{mdb_migration.name}-0" in process_names
    assert f"k8s/{namespace}/{mdb_migration.name}-1" in process_names
    assert f"k8s/{namespace}/{mdb_migration.name}-2" in process_names


@mark.e2e_vm_migration
@skip_if_local
def test_sample_mflix_database_exists_and_not_empty(mongo_tester: MongoTester):
    assert "sample_mflix" in mongo_tester.client.list_database_names(), "sample_mflix database does not exist"
    assert (
        mongo_tester.client["sample_mflix"]["movies"].count_documents({}) > 0
    ), "sample_mflix.movies collection is empty"
