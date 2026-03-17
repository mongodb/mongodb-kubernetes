import yaml
from kubetester import get_statefulset, try_load
from kubetester.kubetester import KubernetesTester, fcv_from_version
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.kubetester import skip_if_local
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.mongotester import MongoDBBackgroundTester, MongoTester
from kubetester.omtester import OMContext, OMTester
from kubetester.operator import Operator
from kubetester.phase import Phase
from pytest import fixture, mark
from tests.common.search import movies_search_helper
from tests.common.search.search_tester import SearchTester
from tests.multicluster.conftest import cluster_spec_list


@fixture(scope="module")
def om_tester(namespace: str) -> OMTester:
    config_map = KubernetesTester.read_configmap(namespace, "my-project")
    secret = KubernetesTester.read_secret(namespace, "my-credentials")
    tester = OMTester(OMContext.build_from_config_map_and_secret(config_map, secret))
    tester.ensure_agent_api_key()
    return tester


@fixture(scope="module")
def vm_sts(namespace: str, om_tester: OMTester, member_cluster_clients):
    with open(yaml_fixture("vm_statefulset.yaml"), "r") as f:
        sts_body = yaml.safe_load(f.read())

        sts_body["spec"]["template"]["spec"]["containers"][0]["env"] = [
            {"name": "MMS_GROUP_ID", "value": om_tester.context.project_id},
            {"name": "MMS_BASE_URL", "value": om_tester.context.base_url},
            {"name": "MMS_API_KEY", "value": om_tester.context.agent_api_key},
        ]

        # Override the command to add overrideLocalHostname since we are not using the headless service hostname.
        sts_body["spec"]["template"]["spec"]["containers"][0]["command"][
            -1
        ] += f" -overrideLocalHost=$(hostname)-svc.{namespace}.svc.cluster.local"

    KubernetesTester.create_or_update_statefulset(
        namespace, body=sts_body, api_client=member_cluster_clients[0].api_client
    )
    return sts_body


@fixture(scope="module")
def vm_services(namespace: str, vm_sts, member_cluster_clients):
    # We can't use headless services for multicluster tests since Istio does not support it. Create one service per pod instead.
    svcs = []
    with open(yaml_fixture("vm_service.yaml"), "r") as f:
        service_body = yaml.safe_load(f.read())
        for i in range(vm_sts["spec"]["replicas"]):
            service_body["metadata"]["name"] = f"vm-mongodb-{i}-svc"
            service_body["spec"]["selector"] = {
                "statefulset.kubernetes.io/pod-name": f"{vm_sts['metadata']['name']}-{i}"
            }
            svcs.append(service_body["metadata"]["name"])
            KubernetesTester.create_or_update_service(
                namespace, body=service_body, api_client=member_cluster_clients[0].api_client
            )

    return svcs


@fixture(scope="module")
def mdb_migration(namespace: str, custom_mdb_version: str, vm_sts, vm_services, member_cluster_names) -> MongoDBMulti:
    resource = MongoDBMulti.from_yaml(yaml_fixture("mongodb-multi.yaml"), namespace=namespace)

    if try_load(resource):
        return resource

    resource.set_version(custom_mdb_version)
    resource["spec"]["replicaSetNameOverride"] = f"{vm_sts['metadata']['name']}-rs"

    resource["spec"]["externalMembers"] = []
    for i in range(vm_sts["spec"]["replicas"]):
        resource["spec"]["externalMembers"].append(
            {
                "processName": f"{vm_sts['metadata']['name']}-{i}",
                "hostname": f"{vm_services[i]}.{namespace}.svc.cluster.local",
                "type": "mongod",
                "replicaSetName": f"{vm_sts['metadata']['name']}-rs",
            }
        )

    resource["spec"]["persistent"] = False
    resource["spec"]["clusterSpecList"] = cluster_spec_list(
        member_cluster_names,
        [1, 1, 1],
        member_configs=[
            [
                {
                    "votes": 0,
                    "priority": "0",
                },
            ],
            [
                {
                    "votes": 0,
                    "priority": "0",
                }
            ],
            [
                {
                    "votes": 0,
                    "priority": "0",
                },
            ],
        ],
    )

    return resource


@fixture(scope="module")
def mongo_tester(mdb_migration: MongoDBMulti):
    return mdb_migration.tester()


@fixture(scope="module")
def mdb_health_checker(mongo_tester: MongoTester) -> MongoDBBackgroundTester:
    health_checker = MongoDBBackgroundTester(mongo_tester)
    health_checker.start()
    return health_checker


@mark.e2e_vm_migration_multicluster_rs
def test_deploy_vm(namespace: str, vm_sts, vm_services, member_cluster_clients):
    def sts_is_ready():
        sts = get_statefulset(namespace, vm_sts["metadata"]["name"], api_client=member_cluster_clients[0].api_client)
        return sts.status.ready_replicas == 3

    KubernetesTester.wait_until(sts_is_ready, timeout=300)


@mark.e2e_vm_migration_multicluster_rs
def test_update_vm_ac(namespace: str, om_tester: OMTester, vm_sts, vm_services, custom_mdb_version):
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
                "hostname": f"{vm_services[i]}.{namespace}.svc.cluster.local",
                "logPath": "/var/log/mongodb-mms-automation/monitoring-agent.log",
                "logRotate": {"sizeThresholdMB": 1000, "timeThresholdHrs": 24},
            }
        )

        ac["backupVersions"].append(
            {
                "hostname": f"{vm_services[i]}.{namespace}.svc.cluster.local",
                "logPath": "/var/log/mongodb-mms-automation/backup-agent.log",
                "logRotate": {"sizeThresholdMB": 1000, "timeThresholdHrs": 24},
            }
        )

        ac["processes"].append(
            {
                "version": custom_mdb_version,
                "name": f"{vm_sts['metadata']['name']}-{i}",
                "hostname": f"{vm_services[i]}.{namespace}.svc.cluster.local",
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


@mark.e2e_vm_migration_multicluster_rs
def test_vm_deployment_is_ready(om_tester: OMTester, vm_sts):
    om_tester.wait_agents_ready()
    ac_tester = om_tester.get_automation_config_tester()

    assert len(ac_tester.get_all_processes()) == 3
    assert len(ac_tester.get_monitoring_versions()) == 3
    assert len(ac_tester.get_backup_versions()) == 3
    assert len(ac_tester.get_replica_set_processes(f"{vm_sts['metadata']['name']}-rs")) == 3


@mark.e2e_vm_migration_multicluster_rs
@skip_if_local
def test_insert_sample_data(om_tester: OMTester, vm_sts, namespace):
    sample_movies_helper = movies_search_helper.SampleMoviesSearchHelper(
        SearchTester(
            f"mongodb://{vm_sts['metadata']['name']}-0-svc.{namespace}.svc.cluster.local:27017/?replicaSet={vm_sts['metadata']['name']}-rs"
        )
    )
    sample_movies_helper.restore_sample_database()


@mark.e2e_vm_migration_multicluster_rs
def test_deploy_operator(multi_cluster_operator: Operator):
    multi_cluster_operator.assert_is_running()


@mark.e2e_vm_migration_multicluster_rs
def test_mdb_reaches_running(mdb_migration: MongoDBMulti, om_tester: OMTester, vm_sts):
    mdb_migration.update()
    mdb_migration.assert_reaches_phase(Phase.Running, timeout=600)

    ac_tester = om_tester.get_automation_config_tester()
    assert len(ac_tester.get_all_processes()) == 6
    assert len(ac_tester.get_monitoring_versions()) == 6
    assert len(ac_tester.get_backup_versions()) == 6
    assert len(ac_tester.get_replica_set_processes(f"{vm_sts['metadata']['name']}-rs")) == 6


# TODO insert sample data, assert it is still there after migration
@mark.e2e_vm_migration_multicluster_rs
def test_promote_and_prune(
    mdb_migration: MongoDBMulti, vm_sts, om_tester: OMTester, mdb_health_checker: MongoDBBackgroundTester
):
    for i in range(vm_sts["spec"]["replicas"]):
        mdb_migration["spec"]["clusterSpecList"][i]["memberConfig"][0]["priority"] = "1"
        mdb_migration["spec"]["clusterSpecList"][i]["memberConfig"][0]["votes"] = 1
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

    mdb_health_checker.assert_healthiness()


@mark.e2e_vm_migration_multicluster_rs
def test_process_names(om_tester: OMTester, namespace, mdb_migration):
    ac_tester = om_tester.get_automation_config_tester()
    process_names = [process["name"] for process in ac_tester.get_all_processes()]
    assert f"k8s/{namespace}/{mdb_migration.name}-0-0" in process_names
    assert f"k8s/{namespace}/{mdb_migration.name}-1-0" in process_names
    assert f"k8s/{namespace}/{mdb_migration.name}-2-0" in process_names


@mark.e2e_vm_migration_multicluster_rs
@skip_if_local
def test_sample_mflix_database_exists_and_not_empty(mongo_tester: MongoTester):
    assert "sample_mflix" in mongo_tester.client.list_database_names(), "sample_mflix database does not exist"
    assert (
        mongo_tester.client["sample_mflix"]["movies"].count_documents({}) > 0
    ), "sample_mflix.movies collection is empty"
