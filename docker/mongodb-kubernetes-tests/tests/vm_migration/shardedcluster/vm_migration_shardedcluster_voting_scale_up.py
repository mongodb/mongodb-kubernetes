"""
VM migration for a sharded cluster where the customer scales a component from 0 to several
voting Kubernetes members in a single update.

The replica set equivalent works: MongoDB.ForcedIndividualScaling forces one-member-per-reconcile
scaling while externalMembers are present, so every automation config push adds at most one voting
member to the VM replica set. This scenario asserts the same holds for a sharded cluster:

  1. The generated CR is applied with configServerCount 0 -> 3, every Kubernetes config server
     member voting (the customer edits the generated YAML before the first apply).
  2. Once the config server is healthy, mongodsPerShardCount goes 0 -> 3 with every Kubernetes shard
     member voting in the shard's override.

Both steps assert the replica set grows by exactly one member per automation config push. If the
operator instead adds all Kubernetes members at once, Ops Manager rejects the deployment with
"Cannot add/remove multiple voting members of a replica set at once" and the resource goes to
Failed.
"""

from kubetester import get_statefulset, try_load
from kubetester.kubetester import KubernetesTester, ensure_ent_version
from kubetester.mongodb import INTERMEDIATE_EVENTS, MongoDB
from kubetester.mongodb_utils_state import in_desired_state
from kubetester.omtester import OMContext, OMTester
from kubetester.operator import Operator
from kubetester.phase import Phase
from pytest import fixture, mark
from tests.vm_migration.vm_migration_common_helper import (
    assert_migration_data_exists,
    generated_mongodb_doc,
    insert_migration_data,
    run_generate_cr,
)
from tests.vm_migration.vm_migration_dry_run import run_migration_dry_run_connectivity_passes
from tests.vm_migration.vm_migration_sharded_helper import (
    MIN_VM_CONFIGSRV,
    MIN_VM_MONGOS,
    MIN_VM_SHARD,
    _set_shard_member_configs,
    _shard_k8s_name_for_rs,
    build_sharded_cluster_ac,
    deploy_vm_sharded_configsrv_service,
    deploy_vm_sharded_configsrv_statefulset,
    deploy_vm_sharded_mongos_service,
    deploy_vm_sharded_mongos_statefulset,
    deploy_vm_sharded_shard_service,
    deploy_vm_sharded_shard_statefulset,
    sharded_connection_string_tester,
    vm_mongos_tester,
)

CONFIGSRV_STS_NAME = "vm-sharded-configsrv"
SHARD_STS_NAME = "vm-sharded-shard"
MONGOS_STS_NAME = "vm-sharded-mongos"
CONFIGSRV_SVC_NAME = "vm-sharded-configsrv"
SHARD_SVC_NAME = "vm-sharded-shard"
MONGOS_SVC_NAME = "vm-sharded-mongos"
MDB_RESOURCE_NAME = "sharded-migration"
VM_CONFIG_RS_NAME = "vm-config"
VM_SHARD_RS_NAME = "vm-shard-0"
VM_MONGOS_NAME = "vm-mongos"

# Kubernetes members added per component in one update, all voting. Stays within the 7 voting
# member limit together with the VM members (3 + 3 config server, 4 + 3 shard).
K8S_VOTING_MEMBERS = 3
VOTING_MEMBER_CONFIG = [{"votes": 1, "priority": "1"} for _ in range(K8S_VOTING_MEMBERS)]


@fixture(scope="module")
def om_tester(namespace: str) -> OMTester:
    config_map = KubernetesTester.read_configmap(namespace, "my-project")
    secret = KubernetesTester.read_secret(namespace, "my-credentials")
    tester = OMTester(OMContext.build_from_config_map_and_secret(config_map, secret))
    tester.ensure_agent_api_key()
    return tester


@fixture(scope="module")
def vm_sharded_configsrv_sts(namespace: str, om_tester: OMTester):
    return deploy_vm_sharded_configsrv_statefulset(namespace, om_tester)


@fixture(scope="module")
def vm_sharded_shard_sts(namespace: str, om_tester: OMTester):
    return deploy_vm_sharded_shard_statefulset(namespace, om_tester)


@fixture(scope="module")
def vm_sharded_mongos_sts(namespace: str, om_tester: OMTester):
    return deploy_vm_sharded_mongos_statefulset(namespace, om_tester)


@fixture(scope="module")
def vm_sharded_configsrv_service(namespace: str):
    return deploy_vm_sharded_configsrv_service(namespace)


@fixture(scope="module")
def vm_sharded_shard_service(namespace: str):
    return deploy_vm_sharded_shard_service(namespace)


@fixture(scope="module")
def vm_sharded_mongos_service(namespace: str):
    return deploy_vm_sharded_mongos_service(namespace)


@fixture(scope="module")
def generated_cr_yaml(namespace: str) -> str:
    return run_generate_cr(namespace, resource_name_override=MDB_RESOURCE_NAME)


@fixture(scope="module")
def mdb_migration(namespace: str, generated_cr_yaml: str) -> MongoDB:
    """Apply the generated CR the way the customer did: config server scaled straight to
    K8S_VOTING_MEMBERS voting members, shards and mongos left at the generated 0."""
    resource_doc = generated_mongodb_doc(generated_cr_yaml)
    resource = MongoDB(resource_doc["metadata"]["name"], namespace)
    if try_load(resource):
        return resource

    resource_doc["spec"]["configServerCount"] = K8S_VOTING_MEMBERS
    resource_doc["spec"]["memberConfig"] = VOTING_MEMBER_CONFIG
    resource_doc["spec"]["mongodsPerShardCount"] = 0
    resource_doc["spec"]["mongosCount"] = 0
    _set_shard_member_configs(resource_doc, [])

    resource.backing_obj = resource_doc
    resource.update()
    return resource


def assert_voting_k8s_members(om_tester: OMTester, rs_name: str, k8s_sts_name: str, vm_count: int) -> None:
    """The replica set holds every VM member plus K8S_VOTING_MEMBERS voting Kubernetes members.

    Kubernetes process names are k8s/<namespace>/<sts>-<idx>, VM ones are the bare pod name.
    """
    members = om_tester.get_automation_config_tester().get_replica_set_members(rs_name)
    k8s_members = [m for m in members if f"/{k8s_sts_name}-" in m["host"]]
    assert len(members) == vm_count + K8S_VOTING_MEMBERS, f"unexpected {rs_name} members: {members}"
    assert len(k8s_members) == K8S_VOTING_MEMBERS, f"unexpected Kubernetes members in {rs_name}: {members}"
    assert all(m["votes"] == 1 for m in k8s_members), f"Kubernetes members in {rs_name} must vote: {k8s_members}"


def assert_reaches_running_adding_one_member_at_a_time(
    om_tester: OMTester, mdb: MongoDB, rs_name: str, vm_count: int, timeout: int = 1800
) -> None:
    """Wait for Running while recording the replica set size after each automation config push.

    The replica set already exists on the VM members, so a push adding more than one voting member
    is rejected by Ops Manager (HELP-100454) and the resource goes to Failed. Each +1 step takes a
    full pod-and-agent rollout, far longer than the poll interval, so no step can be missed.
    """
    observed_counts = [vm_count]

    def record_member_count(_: MongoDB) -> bool:
        try:
            count = len(om_tester.get_automation_config_tester().get_replica_set_members(rs_name))
        except Exception as e:
            # A transient OM API error (reset, timeout) must not abort the wait.
            print(f"error reading automation config for {rs_name}, retrying: {e}")
            return False
        if count != observed_counts[-1]:
            observed_counts.append(count)
        # Transient Failed phases (INTERMEDIATE_EVENTS) are skipped; a terminal one raises here.
        return in_desired_state(
            current_state=mdb.get_status_phase(),
            desired_state=Phase.Running,
            current_generation=mdb.get_generation(),
            observed_generation=mdb.get_status_observed_generation(),
            current_message=mdb.get_status_message(),
            intermediate_events=INTERMEDIATE_EVENTS,
        )

    # should_raise=False: a timeout is reported by the asserts below with the observed progression.
    reached_running = mdb.wait_for(record_member_count, timeout=timeout, should_raise=False)
    assert reached_running, f"timed out waiting for Running; observed {rs_name} progression: {observed_counts}"
    assert observed_counts == list(range(vm_count, vm_count + K8S_VOTING_MEMBERS + 1)), (
        f"expected {rs_name} to grow one member at a time from {vm_count} to "
        f"{vm_count + K8S_VOTING_MEMBERS} members, observed: {observed_counts}"
    )


@mark.e2e_vm_migration_shardedcluster_voting_scale_up
def test_deploy_vm_sharded(
    namespace: str,
    vm_sharded_configsrv_sts,
    vm_sharded_shard_sts,
    vm_sharded_mongos_sts,
    vm_sharded_configsrv_service,
    vm_sharded_shard_service,
    vm_sharded_mongos_service,
):
    for sts_body in (vm_sharded_configsrv_sts, vm_sharded_shard_sts, vm_sharded_mongos_sts):

        def sts_is_ready():
            sts = get_statefulset(namespace, sts_body["metadata"]["name"])
            return sts.status.ready_replicas == sts_body["spec"]["replicas"]

        KubernetesTester.wait_until(sts_is_ready, timeout=300)


@mark.e2e_vm_migration_shardedcluster_voting_scale_up
def test_configure_ac(
    namespace: str,
    om_tester: OMTester,
    vm_sharded_configsrv_sts,
    vm_sharded_shard_sts,
    vm_sharded_mongos_sts,
    vm_sharded_configsrv_service,
    vm_sharded_shard_service,
    vm_sharded_mongos_service,
    custom_mdb_version: str,
):
    ac = om_tester.api_get_automation_config()
    if len(ac.get("processes", [])) == 0:
        ac = build_sharded_cluster_ac(
            om_tester,
            configsrv_sts_name=CONFIGSRV_STS_NAME,
            shard_sts_name=SHARD_STS_NAME,
            mongos_sts_name=MONGOS_STS_NAME,
            configsrv_service_name=CONFIGSRV_SVC_NAME,
            shard_service_name=SHARD_SVC_NAME,
            mongos_service_name=MONGOS_SVC_NAME,
            namespace=namespace,
            mongodb_version=ensure_ent_version(custom_mdb_version),
            config_rs_name=VM_CONFIG_RS_NAME,
            shard_rs_name=VM_SHARD_RS_NAME,
            config_server_count=MIN_VM_CONFIGSRV,
            shard_count=MIN_VM_SHARD,
            mongos_count=MIN_VM_MONGOS,
            cluster_name=VM_MONGOS_NAME,
        )
        om_tester.api_put_automation_config(ac)

    # Runs even when a previous run already pushed the config: that run may have died before the
    # agents converged, and the migration tests below assume goal state.
    om_tester.wait_agents_ready(timeout=600)


@mark.e2e_vm_migration_shardedcluster_voting_scale_up
def test_install_operator(operator: Operator):
    operator.wait_for_operator_ready()


@mark.e2e_vm_migration_shardedcluster_voting_scale_up
def test_insert_migration_data(namespace: str):
    insert_migration_data(vm_mongos_tester(MONGOS_STS_NAME, MONGOS_SVC_NAME, namespace))


@mark.e2e_vm_migration_shardedcluster_voting_scale_up
def test_migration_dry_run_connectivity_passes(mdb_migration: MongoDB):
    # The generated CR carries the dry-run annotation; clearing it is what starts the scale-up.
    run_migration_dry_run_connectivity_passes(mdb_migration)


@mark.e2e_vm_migration_shardedcluster_voting_scale_up
def test_config_server_scales_up_with_voting_members(mdb_migration: MongoDB, om_tester: OMTester):
    """configServerCount 0 -> 3 with voting members in the first apply must reach Running,
    adding members to the config server replica set one at a time."""
    assert_reaches_running_adding_one_member_at_a_time(om_tester, mdb_migration, VM_CONFIG_RS_NAME, MIN_VM_CONFIGSRV)
    assert_voting_k8s_members(om_tester, VM_CONFIG_RS_NAME, f"{MDB_RESOURCE_NAME}-config", vm_count=MIN_VM_CONFIGSRV)
    om_tester.assert_cluster_available(VM_MONGOS_NAME)


@mark.e2e_vm_migration_shardedcluster_voting_scale_up
def test_shard_scales_up_with_voting_members(mdb_migration: MongoDB, om_tester: OMTester):
    """mongodsPerShardCount 0 -> 3 with voting members in one update must reach Running."""
    try_load(mdb_migration)
    shard_k8s_name = _shard_k8s_name_for_rs(mdb_migration, VM_SHARD_RS_NAME)
    mdb_migration["spec"]["mongodsPerShardCount"] = K8S_VOTING_MEMBERS
    override = next(o for o in mdb_migration["spec"]["shardOverrides"] if shard_k8s_name in o["shardNames"])
    override["memberConfig"] = VOTING_MEMBER_CONFIG
    mdb_migration.update()

    assert_reaches_running_adding_one_member_at_a_time(om_tester, mdb_migration, VM_SHARD_RS_NAME, MIN_VM_SHARD)
    assert_voting_k8s_members(om_tester, VM_SHARD_RS_NAME, shard_k8s_name, vm_count=MIN_VM_SHARD)
    om_tester.assert_cluster_available(VM_MONGOS_NAME)


@mark.e2e_vm_migration_shardedcluster_voting_scale_up
def test_migration_data_exists(mdb_migration: MongoDB):
    assert_migration_data_exists(sharded_connection_string_tester(mdb_migration))
