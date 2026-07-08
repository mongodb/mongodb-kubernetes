"""Replica-set-specific helpers for VM-to-Kubernetes migration tests.

Deploys the single VM replica set StatefulSet, applies the generated MongoDB CR, and
asserts replica set connection strings and process names. Shared primitives live in
vm_migration_common_helper.
"""

from typing import Optional

import yaml
from kubetester import try_load
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB
from kubetester.mongotester import MongoTester, build_mongodb_connection_uri
from kubetester.omtester import OMTester
from kubetester.phase import Phase
from tests.vm_migration.vm_migration_common_helper import (
    _deploy_vm_statefulset_from_fixture,
    assert_migration_dry_run_annotation,
    assert_migration_tool_version_annotation,
    generated_mongodb_doc,
)
from tests.vm_migration.vm_migration_dry_run import (
    MIGRATING_CONDITION_REASON_EXTENDING,
    MIGRATING_CONDITION_REASON_IN_PROGRESS,
    MIGRATING_CONDITION_REASON_PRUNING,
    wait_until_migrating_condition_reason,
    wait_until_phase_and_migrating_condition_reason,
    wait_until_running_and_migration_complete,
)

# minimum K8s StatefulSet members deployed alongside VM external members.
# MIN_K8S_MONGOD must exceed 7 when added to the external member count so the voting-limit validation always runs.
MIN_K8S_MONGOD = 5
MIN_VM_MONGOD = 3


def deploy_vm_statefulset(
    namespace: str,
    om_tester: OMTester,
    extra_volumes=None,
    extra_volume_mounts=None,
    extra_command_args="",
    replicas: int = MIN_VM_MONGOD,
):
    """Create or update the VM agent StatefulSet with OM credentials.

    Returns the StatefulSet body dict.
    """
    return _deploy_vm_statefulset_from_fixture(
        "vm_statefulset.yaml",
        namespace,
        om_tester,
        extra_volumes=extra_volumes,
        extra_volume_mounts=extra_volume_mounts,
        extra_command_args=extra_command_args,
        replicas=replicas,
    )


def deploy_vm_service(namespace: str):
    """Create or update the VM headless service. Returns the Service body dict."""
    with open(yaml_fixture("vm_service.yaml"), "r") as f:
        service_body = yaml.safe_load(f.read())
    KubernetesTester.create_or_update_service(namespace, body=service_body)
    return service_body


def apply_generated_mongodb_resource(
    namespace: str,
    generated_cr_yaml: str | dict,
    *,
    resource_name: str | None = None,
    customer_sets_disabled_tls_mode: bool = False,
    prepare_external_resources=None,
    initial_members: int | None = None,
) -> MongoDB:
    """Apply the generated MongoDB CR.

    By default all K8s members are pre-provisioned (votes/priority 0) alongside the VM external
    members; ``promote_and_prune`` then re-prioritizes and prunes. Pass ``initial_members=0`` to
    start VM-only and grow ``spec.members`` incrementally via ``promote_and_prune_extend`` (the
    extend-then-prune flow that exercises the ``Migrating`` reason ``Extending``).
    """
    resource_doc = (
        generated_cr_yaml if isinstance(generated_cr_yaml, dict) else generated_mongodb_doc(generated_cr_yaml)
    )
    resource = MongoDB(resource_name or resource_doc["metadata"]["name"], namespace)
    if try_load(resource):
        return resource

    if customer_sets_disabled_tls_mode:
        # The import tool warns about this but does not own changing no-TLS deployments.
        resource_doc.setdefault("spec", {}).setdefault("additionalMongodConfig", {}).setdefault("net", {}).setdefault(
            "tls", {}
        )["mode"] = "disabled"

    external_count = len(resource_doc["spec"].get("externalMembers", []))
    num_members = external_count if initial_members is None else initial_members
    if initial_members is None:
        num_members = max(external_count, MIN_K8S_MONGOD)
    resource_doc["spec"]["members"] = num_members
    resource_doc["spec"]["memberConfig"] = [{"votes": 0, "priority": "0"} for _ in range(num_members)]

    if prepare_external_resources is not None:
        prepare_external_resources(resource_doc)

    resource.backing_obj = resource_doc
    resource.update()
    return resource


def migration_connection_strings(mdb_migration: MongoDB) -> tuple[str, str]:
    secret = KubernetesTester.read_secret(mdb_migration.namespace, f"{mdb_migration.name}-connection-string")
    return secret.get("connectionString.standard", ""), secret.get("connectionString.standardSrv", "")


def k8s_hostnames(mdb_migration: MongoDB) -> list[str]:
    service_name = f"{mdb_migration.name}-svc"
    return [
        f"{mdb_migration.name}-{i}.{service_name}.{mdb_migration.namespace}.svc.cluster.local:27017"
        for i in range(mdb_migration.get_members())
    ]


def assert_connection_string_contains_current_hosts(mdb_migration: MongoDB) -> None:
    conn_str, _ = migration_connection_strings(mdb_migration)
    for hostname in k8s_hostnames(mdb_migration):
        assert hostname in conn_str, f"k8s hostname {hostname!r} missing from connection string secret"
    for external_member in mdb_migration["spec"].get("externalMembers", []):
        assert (
            external_member["hostname"] in conn_str
        ), f"external member {external_member['hostname']!r} missing from connection string secret"


def assert_connection_string_after_full_migration(mdb_migration: MongoDB) -> None:
    assert not mdb_migration["spec"].get("externalMembers"), "expected all external members to be pruned by now"
    conn_str, conn_srv = migration_connection_strings(mdb_migration)
    replica_set_name = mdb_migration["spec"].get("replicaSetNameOverride", mdb_migration.name)
    assert conn_str.startswith("mongodb://"), "connection string must use mongodb:// scheme"
    for hostname in k8s_hostnames(mdb_migration):
        assert hostname in conn_str, f"k8s hostname {hostname!r} missing from final connection string"
    assert f"replicaSet={replica_set_name}" in conn_str

    assert conn_srv.startswith("mongodb+srv://"), "SRV connection string must use mongodb+srv:// scheme"
    assert f"{mdb_migration.get_service()}.{mdb_migration.namespace}.svc.cluster.local" in conn_srv
    assert f"replicaSet={replica_set_name}" in conn_srv


def assert_k8s_process_names(om_tester: OMTester, mdb_migration: MongoDB) -> None:
    ac_tester = om_tester.get_automation_config_tester()
    process_names = [process["name"] for process in ac_tester.get_all_processes()]
    for i in range(mdb_migration.get_members()):
        assert f"k8s/{mdb_migration.namespace}/{mdb_migration.name}-{i}" in process_names


def promote_and_prune(mdb_migration, vm_sts):
    """Promote each Kubernetes member to a voting member and prune one VM member from externalMembers at a time."""
    try_load(mdb_migration)
    for i in range(vm_sts["spec"]["replicas"]):
        mdb_migration["spec"]["memberConfig"][i]["priority"] = "1"
        mdb_migration["spec"]["memberConfig"][i]["votes"] = 1
        mdb_migration.update()
        mdb_migration.assert_reaches_phase(Phase.Running, timeout=1200)
        assert_connection_string_contains_current_hosts(mdb_migration)

        mdb_migration["spec"]["externalMembers"].pop()
        mdb_migration.update()
        mdb_migration.assert_reaches_phase(Phase.Running, timeout=1200)
        assert_connection_string_contains_current_hosts(mdb_migration)

    # Once every VM external member has been pruned the operator marks the migration finished:
    # Migrating=False (reason MigrationComplete) and migrationObservedExternalMembersCount unset.
    wait_until_running_and_migration_complete(mdb_migration)


def promote_and_prune_extend(mdb_migration, vm_sts):
    """Incremental VM → K8s cutover: grow spec.members one at a time, then prune one VM per step.

    Unlike promote_and_prune (which pre-provisions all K8s members and only re-prioritizes), this
    starts VM-only (spec.members == 0) and adds a K8s member per iteration, exercising the
    ``Migrating`` lifecycle reasons the operator emits: ``Extending`` when a member is added,
    ``Pruning`` when a VM member is removed, ``InProgress`` once counts stabilize, and finally
    ``MigrationComplete`` after the last VM member is pruned.
    """
    try_load(mdb_migration)
    if not isinstance(mdb_migration["spec"].get("memberConfig"), list):
        mdb_migration["spec"]["memberConfig"] = []

    # externalMembers shrinks each iteration; snapshot the count so reruns stay bounded.
    total_vms = len(mdb_migration["spec"]["externalMembers"])
    for i in range(total_vms):
        is_last = i == total_vms - 1

        # --- Extend: add one K8s member pinned to votes/priority 0 ---
        mdb_migration["spec"]["members"] = i + 1
        if len(mdb_migration["spec"]["memberConfig"]) <= i:
            mdb_migration["spec"]["memberConfig"].append({"priority": "0", "votes": 0})
        else:
            mdb_migration["spec"]["memberConfig"][i] = {"priority": "0", "votes": 0}
        mdb_migration.update()
        wait_until_migrating_condition_reason(mdb_migration, MIGRATING_CONDITION_REASON_EXTENDING, timeout=600)
        mdb_migration.assert_reaches_phase(Phase.Running, timeout=1200)
        assert_connection_string_contains_current_hosts(mdb_migration)

        # --- Prune: remove one VM member ---
        mdb_migration["spec"]["externalMembers"].pop()
        mdb_migration.update()
        if is_last:
            wait_until_running_and_migration_complete(mdb_migration)
        else:
            # Pruning is a single-reconcile transient state — check Running + Pruning atomically in
            # one poll so the next reconcile flipping it to InProgress can't race the assertion.
            wait_until_phase_and_migrating_condition_reason(
                mdb_migration, "Running", MIGRATING_CONDITION_REASON_PRUNING, timeout=600
            )

        # --- Re-prioritize: restore full votes/priority for the promoted member ---
        mdb_migration["spec"]["memberConfig"][i] = {"priority": "1", "votes": 1}
        mdb_migration.update()
        if is_last:
            mdb_migration.assert_reaches_phase(Phase.Running, timeout=1200)
        else:
            wait_until_phase_and_migrating_condition_reason(
                mdb_migration, "Running", MIGRATING_CONDITION_REASON_IN_PROGRESS, timeout=600
            )
        assert_connection_string_contains_current_hosts(mdb_migration)


def vm_replica_set_tester(namespace: str, use_ssl: bool = False, ca_path: Optional[str] = None) -> MongoTester:
    """Return a MongoTester pointed at the VM StatefulSet replica set (vm-mongodb service)."""
    cnx_string = build_mongodb_connection_uri(
        mdb_resource="vm-mongodb",
        namespace=namespace,
        members=MIN_VM_MONGOD,
        port="27017",
        servicename="vm-mongodb",
    )
    return MongoTester(cnx_string, use_ssl, ca_path)


def assert_generated_external_members(generated_cr: dict, expected_count: int = 3) -> None:
    external_members = generated_cr["spec"]["externalMembers"]
    assert (
        len(external_members) == expected_count
    ), f"Expected {expected_count} external members, got {len(external_members)}"
    for external_member in external_members:
        assert isinstance(external_member, dict), f"externalMember should be a dict, got {type(external_member)}"
        for key in ("processName", "hostname", "type", "replicaSetName"):
            assert key in external_member, f"Missing key {key!r} in externalMember: {external_member}"
        assert external_member["type"] == "mongod"


def assert_generated_member_config_omitted(generated_cr: dict) -> None:
    assert (
        "memberConfig" not in generated_cr["spec"]
    ), "Generated CR should not contain memberConfig. Customers set it when expanding."


def assert_common_generated_cr_shape(
    generated_cr_yaml: str, generated_cr: dict, version_id: str, expected_external_members: int = 3
) -> None:
    assert_migration_dry_run_annotation(generated_cr_yaml)
    assert_migration_tool_version_annotation(generated_cr, version_id)
    assert_generated_external_members(generated_cr, expected_count=expected_external_members)
    assert_generated_member_config_omitted(generated_cr)
