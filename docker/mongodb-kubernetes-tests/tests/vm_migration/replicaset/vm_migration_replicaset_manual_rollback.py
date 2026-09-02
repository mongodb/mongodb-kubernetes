"""Deleting a mid-migration MongoDB resource must not touch Ops Manager.

This covers the accident, not the procedure. Deleting the CR is NOT how a VM-to-Kubernetes
migration should be rolled back: it also tears down the StatefulSet, and once enough Kubernetes
members have been promoted to voting, losing their pods can drop the replica set below a
majority. The supported rollback is to stop the operator (scale its Deployment to zero) and edit
the automation config by hand while the StatefulSets are still running, so the members stay
available while they are removed from the replica set.

What the operator guards against is somebody deleting the CR anyway -- by mistake, or by
`kubectl delete -f` on a whole directory. The replica set in Ops Manager is then partly made of
processes the operator does not own, and the normal cleanup path would take the VM-hosted
processes with it: om.Deployment.RemoveReplicaSetByName iterates every member with no ownership
filter. ReplicaSetReconcilerHelper.cleanOpsManagerState therefore returns early whenever
spec.externalMembers is non-empty, leaving Ops Manager alone. A live VM deployment survives the
mistake.

This test provokes that mistake and asserts the blast radius is zero: bring up 3 VM members plus
one non-voting Kubernetes member, delete the resource, and check the automation config is
byte-identical. It then cleans up the entry the deleted pod left behind, confirming the
deployment is back to its original three-VM shape, healthy, with its data intact.

The single Kubernetes member here is deliberately non-voting (votes/priority 0), so the three VM
members keep the whole majority and deleting the pod cannot cost the replica set its primary.
That is what makes this scenario safe to provoke in a test; it is not a claim that deleting the
CR is safe in general.
"""

from kubernetes.client.rest import ApiException
from kubetester import get_statefulset, try_load
from kubetester.kubetester import KubernetesTester, ensure_ent_version, fcv_from_version, skip_if_local
from kubetester.mongodb import MongoDB
from kubetester.omtester import OMContext, OMTester
from kubetester.operator import Operator
from kubetester.phase import Phase
from pytest import fixture, mark
from tests.vm_migration.vm_migration_common_helper import (
    K8S_PROCESS_NAME_PREFIX,
    assert_automation_config_unchanged,
    assert_migration_data_exists,
    generated_mongodb_doc,
    insert_migration_data,
    k8s_process_name,
    remove_k8s_member_from_automation_config,
    run_generate_cr,
    wait_for_automation_config_quiescence,
)
from tests.vm_migration.vm_migration_dry_run import run_migration_dry_run_connectivity_passes
from tests.vm_migration.vm_migration_replicaset_helper import (
    MIN_VM_MONGOD,
    apply_generated_mongodb_resource,
    assert_connection_string_contains_current_hosts,
    deploy_vm_service,
    deploy_vm_statefulset,
    vm_replica_set_tester,
)


@fixture(scope="module")
def om_tester(namespace: str) -> OMTester:
    config_map = KubernetesTester.read_configmap(namespace, "my-project")
    secret = KubernetesTester.read_secret(namespace, "my-credentials")
    tester = OMTester(OMContext.build_from_config_map_and_secret(config_map, secret))
    tester.ensure_agent_api_key()
    return tester


@fixture(scope="module")
def vm_sts(namespace: str, om_tester: OMTester):
    return deploy_vm_statefulset(namespace, om_tester)


@fixture(scope="module")
def vm_service(namespace: str):
    return deploy_vm_service(namespace)


def _configure_ac_no_auth(namespace: str, om_tester: OMTester, vm_sts: dict, vm_service: dict, mdb_version: str):
    """Set up a replica set with auth DISABLED. Mirrors the no_auth migration scenario."""
    mdb_version = ensure_ent_version(mdb_version)
    ac = om_tester.api_get_automation_config()
    if len(ac["processes"]) > 0:
        return

    sts_name = vm_sts["metadata"]["name"]
    svc_name = vm_service["metadata"]["name"]
    rs_name = f"{sts_name}-rs"

    ac["auth"] = {"disabled": True, "authoritativeSet": False}

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
                        "tls": {"mode": "disabled"},
                        "compression": {"compressors": "snappy,zstd"},
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
    return run_generate_cr(namespace)


@fixture(scope="module")
def generated_cr(generated_cr_yaml: str) -> dict:
    return generated_mongodb_doc(generated_cr_yaml)


@fixture(scope="module")
def mdb_migration(namespace: str, generated_cr: dict) -> MongoDB:
    # Apply straight at one K8s member rather than growing from zero: the point of this test is
    # what deletion does to Ops Manager, and a single non-voting member is already enough to make
    # the deployment mixed VM + K8s. externalMembers stays at 3 and is never pruned, so the
    # resource is unambiguously mid-migration when it gets deleted.
    return apply_generated_mongodb_resource(
        namespace, generated_cr, customer_sets_disabled_tls_mode=True, initial_members=1
    )


# Module-scoped mutable state: the AC snapshot taken just before deletion, and the resource name,
# both needed by tests that run after mdb_migration has been deleted (the fixture object is stale
# by then, and re-instantiating it would fail).
@fixture(scope="module")
def rollback_state() -> dict:
    return {}


# --- Setup: bring up the VM deployment, exactly as the other migration scenarios do ---


@mark.e2e_vm_migration_replicaset_manual_rollback
def test_deploy_vm(namespace: str, vm_sts, vm_service):
    def sts_is_ready():
        sts = get_statefulset(namespace, vm_sts["metadata"]["name"])
        return sts.status.ready_replicas == vm_sts["spec"]["replicas"]

    KubernetesTester.wait_until(sts_is_ready, timeout=300)


@mark.e2e_vm_migration_replicaset_manual_rollback
def test_configure_ac(namespace: str, om_tester: OMTester, vm_sts, vm_service, custom_mdb_version):
    _configure_ac_no_auth(namespace, om_tester, vm_sts, vm_service, custom_mdb_version)
    om_tester.wait_agents_ready(timeout=600)


@mark.e2e_vm_migration_replicaset_manual_rollback
def test_install_operator(operator: Operator):
    operator.wait_for_operator_ready()


@mark.e2e_vm_migration_replicaset_manual_rollback
def test_insert_migration_data(namespace: str):
    insert_migration_data(vm_replica_set_tester(namespace))


# --- Begin the migration: one K8s member alongside the three VM members, and stop there ---


@mark.e2e_vm_migration_replicaset_manual_rollback
def test_migration_dry_run_connectivity_passes(mdb_migration: MongoDB):
    """Validate connectivity to the VM members, then clear the dry-run annotation.

    The generated CR ships with mongodb.com/migration-dry-run=true and the operator never removes
    it -- this helper does (vm_migration_dry_run.py:142-146). Without it the resource would sit in
    Migrating reason ``Validating`` forever and never provision a pod.
    """
    run_migration_dry_run_connectivity_passes(mdb_migration)


@mark.e2e_vm_migration_replicaset_manual_rollback
def test_migration_reaches_running(mdb_migration: MongoDB, rollback_state: dict):
    """The resource comes up with a single non-voting K8s member and all 3 VM members intact.

    apply_generated_mongodb_resource pins the member to votes/priority 0, so the three VM members
    still hold the entire voting majority. That is what makes the rollback safe: losing the K8s
    pod later cannot cost the replica set its primary.
    """
    rollback_state["resource_name"] = mdb_migration.name
    mdb_migration.assert_reaches_phase(Phase.Running, timeout=1200)
    assert_connection_string_contains_current_hosts(mdb_migration)

    assert len(mdb_migration["spec"]["externalMembers"]) == MIN_VM_MONGOD, (
        "externalMembers must be untouched -- this scenario never prunes, so the resource is "
        "still mid-migration when it gets deleted"
    )


@mark.e2e_vm_migration_replicaset_manual_rollback
def test_automation_config_has_both_vm_and_k8s_processes(namespace: str, om_tester: OMTester, rollback_state: dict):
    """Precondition for the whole test: OM now describes a mixed 3-VM + 1-K8s replica set."""
    ac = om_tester.api_get_automation_config()
    names = [p["name"] for p in ac["processes"]]

    expected_k8s = k8s_process_name(namespace, rollback_state["resource_name"], 0)
    assert expected_k8s in names, f"expected operator-managed process {expected_k8s} in {names}"

    vm_names = [n for n in names if not n.startswith(K8S_PROCESS_NAME_PREFIX)]
    assert len(vm_names) == MIN_VM_MONGOD, f"expected {MIN_VM_MONGOD} VM processes, got {vm_names}"
    assert len(names) == MIN_VM_MONGOD + 1, f"expected exactly 4 processes total, got {names}"


# --- Provoke the accident: delete the resource while it is still mid-migration ---


@mark.e2e_vm_migration_replicaset_manual_rollback
def test_delete_mongodb_resource(mdb_migration: MongoDB, om_tester: OMTester, rollback_state: dict):
    """Snapshot Ops Manager, then delete the CR while it still declares externalMembers.

    Stands in for an accidental deletion. The supported rollback stops the operator and edits the
    automation config with the StatefulSets still up; this deliberately does the unsupported thing
    to prove the operator's guard holds.
    """
    rollback_state["ac_before_delete"] = wait_for_automation_config_quiescence(om_tester)

    namespace = mdb_migration.namespace
    name = mdb_migration.name
    mdb_migration.delete()

    def resource_is_gone() -> bool:
        # try_load returns False on a 404 and re-raises anything else.
        return not try_load(MongoDB(name, namespace))

    KubernetesTester.wait_until(resource_is_gone, timeout=300)


@mark.e2e_vm_migration_replicaset_manual_rollback
def test_automation_config_unchanged_after_delete(om_tester: OMTester, rollback_state: dict):
    """The core assertion: an accidental delete must not touch Ops Manager at all.

    Not just the VM processes -- nothing. The operator's own K8s process stays too, because
    cleanOpsManagerState returns early rather than removing the replica set (which would take the
    VM processes with it: om.Deployment.RemoveReplicaSetByName has no ownership filter). Leaving
    the orphaned entry behind is the correct trade: a stale automation config entry is repairable
    by hand, a deleted VM deployment is not.

    Deletion cleanup is a one-shot, best-effort watch handler with no completion signal, so this
    polls over a window rather than checking once.
    """
    assert_automation_config_unchanged(om_tester, rollback_state["ac_before_delete"], duration=90, interval=10)


@mark.e2e_vm_migration_replicaset_manual_rollback
def test_k8s_statefulset_is_gone(namespace: str, rollback_state: dict):
    """Kubernetes garbage-collects the migration StatefulSet, orphaning the AC's K8s process.

    This is precisely why deleting the CR is the wrong way to roll back: the pods go with it. Here
    the lost member was non-voting so nothing is at risk, but had voting members been promoted
    already, this step is where the replica set would have lost its majority.

    It also leaves the state the repair below has to clean up: an automation config entry pointing
    at a host that no longer exists.
    """
    name = rollback_state["resource_name"]

    def sts_is_gone() -> bool:
        try:
            get_statefulset(namespace, name)
            return False
        except ApiException as e:
            return e.status == 404

    KubernetesTester.wait_until(sts_is_gone, timeout=300)


# --- Repair the damage: take the orphaned K8s member out of the automation config ---


@mark.e2e_vm_migration_replicaset_manual_rollback
def test_manually_remove_k8s_member_from_automation_config(namespace: str, om_tester: OMTester, rollback_state: dict):
    # Applied at spec.members=1, so index 0 is the only operator-managed member to remove.
    remove_k8s_member_from_automation_config(om_tester, namespace, rollback_state["resource_name"], 0)
    om_tester.wait_agents_ready(timeout=600)


@mark.e2e_vm_migration_replicaset_manual_rollback
def test_automation_config_back_to_vm_only(namespace: str, om_tester: OMTester, vm_sts, rollback_state: dict):
    """The deployment is the three original VM processes again, with no operator residue."""
    ac = om_tester.api_get_automation_config()

    sts_name = vm_sts["metadata"]["name"]
    expected_names = {f"{sts_name}-{i}" for i in range(MIN_VM_MONGOD)}

    actual_names = {p["name"] for p in ac["processes"]}
    assert actual_names == expected_names, f"expected exactly the VM processes {expected_names}, got {actual_names}"

    assert len(ac["replicaSets"]) == 1, f"expected a single replica set, got {ac['replicaSets']}"
    member_hosts = {m["host"] for m in ac["replicaSets"][0]["members"]}
    assert member_hosts == expected_names, f"expected replica set members {expected_names}, got {member_hosts}"

    monitoring_hostnames = {mv.get("hostname") for mv in ac.get("monitoringVersions", [])}
    assert not any(
        h and h.startswith(f"{rollback_state['resource_name']}-") for h in monitoring_hostnames
    ), f"operator-managed hosts still monitored: {monitoring_hostnames}"


@mark.e2e_vm_migration_replicaset_manual_rollback
@skip_if_local()
def test_vm_replica_set_healthy_after_rollback(namespace: str):
    vm_replica_set_tester(namespace).assert_connectivity()


@mark.e2e_vm_migration_replicaset_manual_rollback
def test_migration_data_survived_rollback(namespace: str):
    assert_migration_data_exists(vm_replica_set_tester(namespace))
