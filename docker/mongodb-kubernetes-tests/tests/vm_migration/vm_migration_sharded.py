"""VM migration E2E for sharded clusters: pseudo-VM sharded cluster, then MongoDB CR with externalMembers and promote/prune.

Pseudo-VM pods run the automation agent from the same image tag as AGENT_IMAGE on the operator.
The mongod StatefulSet has 6 replicas: pods 0-2 form the config server RS and pods 3-5 form shard 0.
The mongos StatefulSet has 2 replicas.

All three VM-to-K8s name overrides are exercised:
  - configServerNameOverride: VM config RS name "vm-config" differs from the K8s default.
  - shardNameOverrides: VM shard RS name "vm-shard-0" differs from the K8s default.
  - shardedClusterNameOverride: VM mongos name "vm-mongos" differs from the K8s default.
"""

import yaml
from kubetester import get_statefulset, try_load
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.kubetester import skip_if_local
from kubetester.mongodb import MongoDB
from kubetester.mongotester import MongoDBBackgroundTester, MongoTester
from kubetester.omtester import OMContext, OMTester
from kubetester.operator import Operator
from kubetester.phase import Phase
from pytest import fixture, mark
from tests.common.mongodb_tools_pod import mongodb_tools_pod
from tests.common.search import movies_search_helper
from tests.common.search.search_tester import SearchTester
from tests.conftest import (
    get_central_cluster_client,
    get_central_cluster_name,
    get_default_operator,
    get_member_cluster_clients,
    get_member_cluster_names,
    get_multi_cluster_operator,
    get_multi_cluster_operator_installation_config,
    get_operator_installation_config,
    is_multi_cluster,
)
from tests.tls.vm_migration_dry_run import run_migration_dry_run_connectivity_passes
from tests.tls.vm_migration_sharded_ac import build_sharded_cluster_ac

MONGOD_STS_NAME = "vm-sharded-mongod"
MONGOS_STS_NAME = "vm-sharded-mongos"
MONGOD_SVC_NAME = "vm-sharded-mongod"
MONGOS_SVC_NAME = "vm-sharded-mongos"
CONFIG_SERVER_COUNT = 3
SHARD_COUNT = 3
MONGOS_COUNT = 2

# K8s resource name (must match the name in vm-migration-sharded.yaml).
MDB_RESOURCE_NAME = "sharded-migration"

# VM-side names that differ from the K8s defaults, used to exercise all three name overrides.
VM_CONFIG_RS_NAME = "vm-config"
VM_SHARD_RS_NAME = "vm-shard-0"
VM_MONGOS_NAME = "vm-mongos"

# 7.0.x is the minimum version with aarch64+rhel90 builds, required for Apple Silicon kind clusters.
MONGODB_VERSION = "7.0.14"


@fixture(scope="module")
def operator(namespace: str) -> Operator:
    if is_multi_cluster():
        return get_multi_cluster_operator(
            namespace,
            get_central_cluster_name(),
            get_multi_cluster_operator_installation_config(namespace),
            get_central_cluster_client(),
            get_member_cluster_clients(),
            get_member_cluster_names(),
        )
    else:
        return get_default_operator(namespace, get_operator_installation_config(namespace))


@fixture(scope="module")
def om_tester(namespace: str, operator) -> OMTester:
    config_map = KubernetesTester.read_configmap(namespace, "my-project")
    secret = KubernetesTester.read_secret(namespace, "my-credentials")
    tester = OMTester(OMContext.build_from_config_map_and_secret(config_map, secret))
    tester.ensure_agent_api_key()
    return tester


@fixture(scope="module")
def vm_sharded_mongod_sts(namespace: str, om_tester: OMTester):
    with open(yaml_fixture("vm_sharded_statefulset.yaml"), "r") as f:
        sts_body = yaml.safe_load(f.read())

    sts_body["spec"]["template"]["spec"]["containers"][0]["env"] = [
        {"name": "MMS_GROUP_ID", "value": om_tester.context.project_id},
        {"name": "MMS_BASE_URL", "value": om_tester.context.base_url},
        {"name": "MMS_API_KEY", "value": om_tester.context.agent_api_key},
    ]
    KubernetesTester.create_or_update_statefulset(namespace, body=sts_body)
    return sts_body


@fixture(scope="module")
def vm_sharded_mongos_sts(namespace: str, om_tester: OMTester):
    with open(yaml_fixture("vm_sharded_mongos_statefulset.yaml"), "r") as f:
        sts_body = yaml.safe_load(f.read())

    sts_body["spec"]["template"]["spec"]["containers"][0]["env"] = [
        {"name": "MMS_GROUP_ID", "value": om_tester.context.project_id},
        {"name": "MMS_BASE_URL", "value": om_tester.context.base_url},
        {"name": "MMS_API_KEY", "value": om_tester.context.agent_api_key},
    ]
    KubernetesTester.create_or_update_statefulset(namespace, body=sts_body)
    return sts_body


@fixture(scope="module")
def vm_sharded_service(namespace: str):
    with open(yaml_fixture("vm_sharded_service.yaml"), "r") as f:
        service_body = yaml.safe_load(f.read())

    service_body["spec"]["clusterIP"] = "None"
    KubernetesTester.create_or_update_service(namespace, body=service_body)
    return service_body


@fixture(scope="module")
def vm_sharded_mongos_service(namespace: str):
    with open(yaml_fixture("vm_sharded_mongos_service.yaml"), "r") as f:
        service_body = yaml.safe_load(f.read())

    service_body["spec"]["clusterIP"] = "None"
    KubernetesTester.create_or_update_service(namespace, body=service_body)
    return service_body


@fixture(scope="module")
def mdb_sharded_migration(
    namespace: str,
    vm_sharded_mongod_sts,
    vm_sharded_mongos_sts,
    vm_sharded_service,
    vm_sharded_mongos_service,
) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("vm-migration-sharded.yaml"), namespace=namespace, with_mdb_version_from_env=False
    )

    if try_load(resource):
        return resource

    resource.set_version(MONGODB_VERSION)

    k8s_shard_name = f"{resource.name}-0"

    resource["spec"]["configServerNameOverride"] = VM_CONFIG_RS_NAME
    resource["spec"]["shardedClusterNameOverride"] = VM_MONGOS_NAME
    # AC _id and replicaSetName are both VM_SHARD_RS_NAME, differing from the K8s name k8s_shard_name.
    resource["spec"]["shardNameOverrides"] = [
        {"shardName": k8s_shard_name, "shardId": VM_SHARD_RS_NAME, "replicaSetName": VM_SHARD_RS_NAME}
    ]

    # External members: config server pods 0-2, shard pods 3-5, and mongos pods 0-1.
    resource["spec"]["externalMembers"] = []
    for i in range(CONFIG_SERVER_COUNT):
        resource["spec"]["externalMembers"].append(
            {
                "processName": f"{MONGOD_STS_NAME}-{i}",
                "hostname": f"{MONGOD_STS_NAME}-{i}.{MONGOD_SVC_NAME}.{namespace}.svc.cluster.local:27017",
                "type": "mongod",
                "replicaSetName": VM_CONFIG_RS_NAME,
            }
        )
    for i in range(CONFIG_SERVER_COUNT, CONFIG_SERVER_COUNT + SHARD_COUNT):
        resource["spec"]["externalMembers"].append(
            {
                "processName": f"{MONGOD_STS_NAME}-{i}",
                "hostname": f"{MONGOD_STS_NAME}-{i}.{MONGOD_SVC_NAME}.{namespace}.svc.cluster.local:27017",
                "type": "mongod",
                "replicaSetName": VM_SHARD_RS_NAME,
            }
        )
    for i in range(MONGOS_COUNT):
        resource["spec"]["externalMembers"].append(
            {
                "processName": f"{MONGOS_STS_NAME}-{i}",
                "hostname": f"{MONGOS_STS_NAME}-{i}.{MONGOS_SVC_NAME}.{namespace}.svc.cluster.local:27017",
                "type": "mongos",
                "replicaSetName": "",
            }
        )

    # K8s config server processes start with votes=0, priority=0 so they don't
    # disrupt the VM primary during migration.
    resource["spec"]["memberConfig"] = [{"votes": 0, "priority": "0"} for _ in range(CONFIG_SERVER_COUNT)]

    resource.create()
    return resource


@fixture(scope="module")
def mongo_tester(mdb_sharded_migration: MongoDB) -> MongoTester:
    return mdb_sharded_migration.tester()


@fixture(scope="module")
def mdb_health_checker(mongo_tester: MongoTester) -> MongoDBBackgroundTester:
    health_checker = MongoDBBackgroundTester(mongo_tester)
    health_checker.start()
    return health_checker


@mark.e2e_vm_migration_sharded
def test_deploy_vm_sharded(
    namespace: str, vm_sharded_mongod_sts, vm_sharded_mongos_sts, vm_sharded_service, vm_sharded_mongos_service
):
    def mongod_sts_is_ready():
        sts = get_statefulset(namespace, vm_sharded_mongod_sts["metadata"]["name"])
        return sts.status.ready_replicas == vm_sharded_mongod_sts["spec"]["replicas"]

    def mongos_sts_is_ready():
        sts = get_statefulset(namespace, vm_sharded_mongos_sts["metadata"]["name"])
        return sts.status.ready_replicas == vm_sharded_mongos_sts["spec"]["replicas"]

    KubernetesTester.wait_until(mongod_sts_is_ready, timeout=300)
    KubernetesTester.wait_until(mongos_sts_is_ready, timeout=300)


@mark.e2e_vm_migration_sharded
def test_update_vm_sharded_ac(namespace: str, om_tester: OMTester):
    ac = om_tester.api_get_automation_config()

    if len(ac["processes"]) > 0:
        existing = {p["name"] for p in ac["processes"]}
        if all(f"{MONGOS_STS_NAME}-{i}" in existing for i in range(MONGOS_COUNT)):
            # All VM mongos are present in OM, nothing to restore.
            return
        # VM mongos were removed by a previous operator run. Fall through to restore the full AC.

    ac = build_sharded_cluster_ac(
        om_tester,
        mongod_sts_name=MONGOD_STS_NAME,
        mongos_sts_name=MONGOS_STS_NAME,
        service_name=MONGOD_SVC_NAME,
        mongos_service_name=MONGOS_SVC_NAME,
        namespace=namespace,
        mongodb_version=MONGODB_VERSION,
        config_rs_name=VM_CONFIG_RS_NAME,
        shard_rs_name=VM_SHARD_RS_NAME,
        config_server_count=CONFIG_SERVER_COUNT,
        shard_count=SHARD_COUNT,
        mongos_count=MONGOS_COUNT,
        cluster_name=VM_MONGOS_NAME,
    )
    om_tester.api_put_automation_config(ac)
    om_tester.wait_agents_ready(timeout=1800)


@mark.e2e_vm_migration_sharded
def test_vm_sharded_deployment_is_ready(om_tester: OMTester):
    ac_tester = om_tester.get_automation_config_tester()
    vm_total = CONFIG_SERVER_COUNT + SHARD_COUNT + MONGOS_COUNT
    if len(ac_tester.get_all_processes()) > vm_total:
        # Being retried after the MongoDB CR was already created; VM-only state cannot be re-validated.
        return

    om_tester.wait_agents_ready(timeout=1800)
    ac_tester = om_tester.get_automation_config_tester()
    assert len(ac_tester.get_all_processes()) == vm_total
    assert len(ac_tester.get_monitoring_versions()) == vm_total
    assert len(ac_tester.get_backup_versions()) == vm_total
    assert len(ac_tester.get_sharding_entries()) == 1


@mark.e2e_vm_migration_sharded
@skip_if_local
def test_insert_sample_data(namespace: str):
    sample_movies_helper = movies_search_helper.SampleMoviesSearchHelper(
        SearchTester(f"mongodb://{MONGOS_STS_NAME}-0.{MONGOS_SVC_NAME}.{namespace}.svc.cluster.local:27017/"),
        mongodb_tools_pod.get_tools_pod(namespace),
    )
    sample_movies_helper.restore_sample_database()


@mark.e2e_vm_migration_sharded
def test_install_operator(default_operator: Operator):
    default_operator.assert_is_running()


@mark.e2e_vm_migration_sharded
def test_migration_dry_run_connectivity_passes(mdb_sharded_migration: MongoDB):
    """Set migration-dry-run annotation and wait for NetworkConnectivityVerification condition to become True."""
    run_migration_dry_run_connectivity_passes(mdb_sharded_migration)


@mark.e2e_vm_migration_sharded
def test_mdb_sharded_reaches_running(mdb_sharded_migration: MongoDB, om_tester: OMTester):
    mdb_sharded_migration.assert_reaches_phase(Phase.Running, timeout=1800)

    ac_tester = om_tester.get_automation_config_tester()

    # Each RS should now hold both K8s and VM members.
    ac_tester.assert_sharded_cluster_processes(VM_CONFIG_RS_NAME, [VM_SHARD_RS_NAME], MONGOS_COUNT * 2)

    vm_total = CONFIG_SERVER_COUNT + SHARD_COUNT + MONGOS_COUNT
    k8s_total = (
        mdb_sharded_migration["spec"]["configServerCount"]
        + mdb_sharded_migration["spec"]["mongodsPerShardCount"]
        + mdb_sharded_migration["spec"]["mongosCount"]
    )
    assert len(ac_tester.get_all_processes()) == vm_total + k8s_total


@mark.e2e_vm_migration_sharded
def test_promote_and_prune_config_server(
    mdb_sharded_migration: MongoDB, om_tester: OMTester, mdb_health_checker: MongoDBBackgroundTester
):
    try_load(mdb_sharded_migration)

    # Promote all K8s config server members and prune VM config server members one by one.
    for i in range(CONFIG_SERVER_COUNT):
        mdb_sharded_migration["spec"]["memberConfig"][i]["priority"] = "1"
        mdb_sharded_migration["spec"]["memberConfig"][i]["votes"] = 1
        mdb_sharded_migration.update()
        mdb_sharded_migration.assert_reaches_phase(Phase.Running)

        # Remove the next VM config server external member (they are the first CONFIG_SERVER_COUNT entries).
        config_external = [
            m for m in mdb_sharded_migration["spec"]["externalMembers"] if m["replicaSetName"] == VM_CONFIG_RS_NAME
        ]
        if config_external:
            mdb_sharded_migration["spec"]["externalMembers"].remove(config_external[-1])
            mdb_sharded_migration.update()
            mdb_sharded_migration.assert_reaches_phase(Phase.Running)

        om_tester.assert_cluster_available(VM_MONGOS_NAME)


@mark.e2e_vm_migration_sharded
def test_promote_and_prune_shard(
    mdb_sharded_migration: MongoDB, om_tester: OMTester, mdb_health_checker: MongoDBBackgroundTester
):
    try_load(mdb_sharded_migration)

    # Remove VM shard external members one by one.
    shard_external = [
        m for m in mdb_sharded_migration["spec"]["externalMembers"] if m["replicaSetName"] == VM_SHARD_RS_NAME
    ]
    for _ in range(len(shard_external)):
        current_shard_external = [
            m for m in mdb_sharded_migration["spec"]["externalMembers"] if m["replicaSetName"] == VM_SHARD_RS_NAME
        ]
        if not current_shard_external:
            break
        mdb_sharded_migration["spec"]["externalMembers"].remove(current_shard_external[-1])
        mdb_sharded_migration.update()
        mdb_sharded_migration.assert_reaches_phase(Phase.Running)
        om_tester.assert_cluster_available(VM_MONGOS_NAME)


@mark.e2e_vm_migration_sharded
def test_prune_mongos(mdb_sharded_migration: MongoDB, mdb_health_checker: MongoDBBackgroundTester):
    try_load(mdb_sharded_migration)

    # Remove all remaining VM mongos external members.
    mongos_external = [m for m in mdb_sharded_migration["spec"]["externalMembers"] if m["type"] == "mongos"]
    for m in mongos_external:
        mdb_sharded_migration["spec"]["externalMembers"].remove(m)
    mdb_sharded_migration.update()
    mdb_sharded_migration.assert_reaches_phase(Phase.Running)

    mdb_health_checker.assert_healthiness()


@mark.e2e_vm_migration_sharded
def test_process_names(namespace: str, om_tester: OMTester, mdb_sharded_migration: MongoDB):
    ac_tester = om_tester.get_automation_config_tester()
    process_names = [process["name"] for process in ac_tester.get_all_processes()]

    name = mdb_sharded_migration.name
    for i in range(mdb_sharded_migration["spec"]["configServerCount"]):
        assert f"k8s/{namespace}/{name}-config-{i}" in process_names

    for i in range(mdb_sharded_migration["spec"]["mongodsPerShardCount"]):
        assert f"k8s/{namespace}/{name}-0-{i}" in process_names

    for i in range(mdb_sharded_migration["spec"]["mongosCount"]):
        assert f"k8s/{namespace}/{name}-mongos-{i}" in process_names


@mark.e2e_vm_migration_sharded
@skip_if_local
def test_sample_mflix_database_exists_and_not_empty(mongo_tester: MongoTester):
    assert "sample_mflix" in mongo_tester.client.list_database_names(), "sample_mflix database does not exist"
    assert (
        mongo_tester.client["sample_mflix"]["movies"].count_documents({}) > 0
    ), "sample_mflix.movies collection is empty"
