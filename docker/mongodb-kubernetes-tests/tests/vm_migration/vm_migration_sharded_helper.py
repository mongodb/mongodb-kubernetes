"""Sharded-cluster-specific helpers for VM-to-Kubernetes migration tests.

Deploys the three VM sharded StatefulSets (config server, shard, mongos), builds the
automation config for a pseudo-VM sharded cluster, applies the generated MongoDB CR, and
asserts sharded process names and connectivity. Shared primitives live in
vm_migration_common_helper.
"""

from typing import Optional

import yaml
from kubetester import try_load
from kubetester.kubetester import KubernetesTester, fcv_from_version
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB
from kubetester.mongotester import MongoTester, build_mongodb_connection_uri
from kubetester.omtester import OMTester
from kubetester.phase import Phase
from tests.vm_migration.vm_migration_common_helper import (
    MIGRATION_DRY_RUN_ANNOTATION,
    _deploy_vm_statefulset_from_fixture,
    assert_migration_tool_version_annotation,
    cluster_connection_string_secret_name,
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

# The voting limit test trips the config server and a shard independently: the config server
# votes come from top-level spec.memberConfig and the shard votes from its own shardOverride,
# so making one component's K8s members voting never affects the other. Each K8s count plus
# its VM members is sized to cross the 7 voting member limit on its own. See
# assert_max_voting_members_validation for the arithmetic.
MIN_K8S_CONFIGSRV = 6
MIN_K8S_SHARD = 4
MIN_K8S_MONGOS = 2
MIN_VM_CONFIGSRV = 3
MIN_VM_SHARD = 4
MIN_VM_MONGOS = 2


def deploy_vm_sharded_configsrv_statefulset(
    namespace: str,
    om_tester: OMTester,
    extra_volumes=None,
    extra_volume_mounts=None,
    extra_command_args: str = "",
) -> dict:
    """Create or update the VM config server StatefulSet with OM credentials. Returns the body dict."""
    return _deploy_vm_statefulset_from_fixture(
        "vm_sharded_configsrv_statefulset.yaml",
        namespace,
        om_tester,
        extra_volumes=extra_volumes,
        extra_volume_mounts=extra_volume_mounts,
        extra_command_args=extra_command_args,
        replicas=MIN_VM_CONFIGSRV,
    )


def deploy_vm_sharded_shard_statefulset(
    namespace: str,
    om_tester: OMTester,
    extra_volumes=None,
    extra_volume_mounts=None,
    extra_command_args: str = "",
) -> dict:
    """Create or update the VM shard StatefulSet with OM credentials. Returns the body dict."""
    return _deploy_vm_statefulset_from_fixture(
        "vm_sharded_shard_statefulset.yaml",
        namespace,
        om_tester,
        extra_volumes=extra_volumes,
        extra_volume_mounts=extra_volume_mounts,
        extra_command_args=extra_command_args,
        replicas=MIN_VM_SHARD,
    )


def deploy_vm_sharded_mongos_statefulset(
    namespace: str,
    om_tester: OMTester,
    extra_volumes=None,
    extra_volume_mounts=None,
    extra_command_args: str = "",
) -> dict:
    """Create or update the VM mongos StatefulSet with OM credentials. Returns the body dict."""
    return _deploy_vm_statefulset_from_fixture(
        "vm_sharded_mongos_statefulset.yaml",
        namespace,
        om_tester,
        extra_volumes=extra_volumes,
        extra_volume_mounts=extra_volume_mounts,
        extra_command_args=extra_command_args,
        replicas=MIN_VM_MONGOS,
    )


def deploy_vm_sharded_configsrv_service(namespace: str) -> dict:
    """Create or update the VM config server headless service. Returns the body dict."""
    with open(yaml_fixture("vm_sharded_configsrv_service.yaml"), "r") as f:
        service_body = yaml.safe_load(f.read())
    service_body["spec"]["clusterIP"] = "None"
    KubernetesTester.create_or_update_service(namespace, body=service_body)
    return service_body


def deploy_vm_sharded_shard_service(namespace: str) -> dict:
    """Create or update the VM shard headless service. Returns the body dict."""
    with open(yaml_fixture("vm_sharded_shard_service.yaml"), "r") as f:
        service_body = yaml.safe_load(f.read())
    service_body["spec"]["clusterIP"] = "None"
    KubernetesTester.create_or_update_service(namespace, body=service_body)
    return service_body


def deploy_vm_sharded_mongos_service(namespace: str) -> dict:
    """Create or update the VM mongos headless service. Returns the body dict."""
    with open(yaml_fixture("vm_sharded_mongos_service.yaml"), "r") as f:
        service_body = yaml.safe_load(f.read())
    service_body["spec"]["clusterIP"] = "None"
    KubernetesTester.create_or_update_service(namespace, body=service_body)
    return service_body


def _set_shard_member_configs(resource_doc: dict, member_config: list[dict]) -> None:
    """Give every shard a shardOverride carrying member_config, preserving whatever the generated
    CR already put in that override.

    The merge matters for a heterogeneous source: migrate-to-mck leaves spec.shard absent and
    states each shard's additionalMongodConfig in its own shardOverride, so overwriting the list
    would discard the only copy of that config (sharded_cluster_generator.go, buildShardOverrides).
    Shards the generator skipped -- those whose config could not be determined -- simply get an
    entry with member_config alone.

    buildShardOverrides emits one shardNames element per entry, so keying by shard name loses
    nothing here.
    """
    generated = {name: o for o in resource_doc["spec"].get("shardOverrides", []) for name in o["shardNames"]}
    resource_name_value = resource_doc["metadata"]["name"]
    overrides = []
    for shard_index in range(resource_doc["spec"]["shardCount"]):
        shard_name = f"{resource_name_value}-{shard_index}"
        overrides.append(
            {
                **generated.get(shard_name, {}),
                "shardNames": [shard_name],
                "memberConfig": member_config,
            }
        )
    resource_doc["spec"]["shardOverrides"] = overrides


def apply_generated_sharded_cluster_resource(
    namespace: str,
    generated_cr_yaml: str,
    config_rs_name: str,
    *,
    resource_name: str | None = None,
    customer_sets_disabled_tls_mode: bool = False,
    prepare_external_resources=None,
    incremental: bool = False,
) -> MongoDB:
    """Apply the generated sharded cluster CR. The config server K8s members get their votes from
    top-level memberConfig and each shard gets its votes from a per-shard shardOverride, both non
    voting to start. The per-shard entries augment whatever shardOverrides the generated CR already
    carried rather than replacing them -- see _set_shard_member_configs.

    When incremental=True, all K8s counts start at 0 and grow one member at a time via
    extend_and_prune_sharded_* operations."""
    resource_doc = generated_mongodb_doc(generated_cr_yaml)
    resource = MongoDB(resource_name or resource_doc["metadata"]["name"], namespace)
    if try_load(resource):
        return resource

    if customer_sets_disabled_tls_mode:
        for component in ("configSrv", "shard", "mongos"):
            resource_doc["spec"].setdefault(component, {}).setdefault("additionalMongodConfig", {}).setdefault(
                "net", {}
            ).setdefault("tls", {})["mode"] = "disabled"

    if incremental:
        # VM-only import: every Kubernetes node count starts at 0 and grows one member at a time via
        # extend_and_prune_sharded_* , exercising the Migrating reasons Extending/Pruning. Mirrors the
        # replica set initial_members=0 flow. shardCount keeps its imported value; VM processes stay in
        # externalMembers.
        resource_doc["spec"]["configServerCount"] = 0
        resource_doc["spec"]["mongodsPerShardCount"] = 0
        resource_doc["spec"]["mongosCount"] = 0
        resource_doc["spec"]["memberConfig"] = []
        _set_shard_member_configs(resource_doc, [])
    else:
        # The generated CR carries all Kubernetes node counts at 0, mirroring the replica set Members
        # field, so the customer sets the target Kubernetes counts here. The VM nodes stay in
        # externalMembers and the Kubernetes members scale up from 0.
        resource_doc["spec"]["mongodsPerShardCount"] = MIN_K8S_SHARD
        resource_doc["spec"]["mongosCount"] = MIN_K8S_MONGOS

        # Shard K8s members default to voting, which would already put a shard over the limit once its
        # VM members are counted. A per-shard override pins them non voting and keeps shard votes
        # independent of the config server for the voting limit test.
        _set_shard_member_configs(resource_doc, [{"votes": 0, "priority": "0"} for _ in range(MIN_K8S_SHARD)])

        config_members = [
            m for m in resource_doc["spec"].get("externalMembers", []) if m.get("replicaSetName") == config_rs_name
        ]
        if config_members:
            resource_doc["spec"]["configServerCount"] = MIN_K8S_CONFIGSRV
            resource_doc["spec"]["memberConfig"] = [{"votes": 0, "priority": "0"} for _ in range(MIN_K8S_CONFIGSRV)]

    if prepare_external_resources is not None:
        prepare_external_resources(resource_doc)

    resource.backing_obj = resource_doc
    resource.update()
    return resource


def sharded_connection_string_tester(
    mdb_migration: MongoDB, use_ssl: bool = False, ca_path: str | None = None
) -> MongoTester:
    """Return a MongoTester seeded from the operator-managed <name>-cluster-connection-string secret.

    Unlike mdb_migration.tester() (which builds K8s mongos FQDNs from spec.mongosCount and therefore
    targets nothing while mongosCount == 0), the standard connection string lists the CURRENT active
    mongos routers: the external VM mongos early in migration and the K8s mongos once they exist.
    """
    try_load(mdb_migration)
    secret_name = cluster_connection_string_secret_name(mdb_migration)
    secret = KubernetesTester.read_secret(mdb_migration.namespace, secret_name)
    conn_str = secret.get("connectionString.standard", "")
    assert conn_str, f"connection-string secret {secret_name} has no 'connectionString.standard' value yet"
    return MongoTester(conn_str, use_ssl, ca_path)


def _set_member_config(member_config: list, index: int, *, votes: int, priority: str) -> None:
    """Set (appending if needed) the votes/priority of member_config[index]."""
    entry = {"priority": priority, "votes": votes}
    if len(member_config) <= index:
        member_config.append(entry)
    else:
        member_config[index] = entry


def _remove_one_external(mdb_migration: MongoDB, predicate) -> None:
    """Remove one external member matching predicate (raises if none match)."""
    matching = [m for m in mdb_migration["spec"]["externalMembers"] if predicate(m)]
    assert matching, "expected at least one matching external member to prune"
    mdb_migration["spec"]["externalMembers"].remove(matching[-1])


def _wait_prune_or_complete(mdb_migration: MongoDB, is_last: bool, timeout: int) -> None:
    """After a prune update: assert Running+Pruning, or Running+MigrationComplete on the final VM.

    Pruning is a single-reconcile transient state, so Running and the reason are checked in one poll
    to avoid a race with the next reconcile flipping the reason to InProgress.
    """
    if is_last:
        wait_until_running_and_migration_complete(mdb_migration, timeout=timeout)
    else:
        wait_until_phase_and_migrating_condition_reason(
            mdb_migration, "Running", MIGRATING_CONDITION_REASON_PRUNING, timeout=timeout
        )


def extend_and_prune_sharded_replica_components(
    mdb_migration: MongoDB,
    om_tester: OMTester,
    config_rs_name: str,
    shard_rs_name: str,
    mongos_cluster_name: str,
    total_vms: int,
    timeout: int = 600,
) -> None:
    """Incrementally migrate the config server then the single shard (the replica-set components).

    For each VM member: extend one K8s member (non voting) -> assert Extending; prune one VM ->
    assert Pruning; promote the K8s member to voting -> assert InProgress. Config votes live in
    top-level memberConfig; shard votes live in the shard's shardOverride. Every update is a single
    migration change. Does not migrate mongos; the mongos phase runs last, so it (not this function)
    prunes the final external member and observes MigrationComplete.
    """
    try_load(mdb_migration)
    # pruned tracks VMs removed so far so _wait_prune_or_complete can detect the final external member.
    # Because mongos migrates last and always contributes >= 1 VM, pruned == total_vms is never true
    # inside these config/shard loops — every prune here asserts Pruning, never MigrationComplete.
    pruned = 0

    # ---- Config server: grow configServerCount 0 -> n_config; votes in top-level memberConfig ----
    if not isinstance(mdb_migration["spec"].get("memberConfig"), list):
        mdb_migration["spec"]["memberConfig"] = []
    n_config = len([m for m in mdb_migration["spec"]["externalMembers"] if m.get("replicaSetName") == config_rs_name])
    for i in range(n_config):
        mdb_migration["spec"]["configServerCount"] = i + 1
        _set_member_config(mdb_migration["spec"]["memberConfig"], i, votes=0, priority="0")
        mdb_migration.update()
        wait_until_migrating_condition_reason(mdb_migration, MIGRATING_CONDITION_REASON_EXTENDING, timeout=timeout)
        mdb_migration.assert_reaches_phase(Phase.Running, timeout=timeout)

        pruned += 1
        _remove_one_external(mdb_migration, lambda m: m.get("replicaSetName") == config_rs_name)
        mdb_migration.update()
        _wait_prune_or_complete(mdb_migration, pruned == total_vms, timeout)

        _set_member_config(mdb_migration["spec"]["memberConfig"], i, votes=1, priority="1")
        mdb_migration.update()
        wait_until_phase_and_migrating_condition_reason(
            mdb_migration, "Running", MIGRATING_CONDITION_REASON_IN_PROGRESS, timeout=timeout
        )
        om_tester.assert_cluster_available(mongos_cluster_name)

    # ---- Shard: grow mongodsPerShardCount 0 -> n_shard; votes in the shard's shardOverride ----
    shard_k8s_name = _shard_k8s_name_for_rs(mdb_migration, shard_rs_name)
    n_shard = len([m for m in mdb_migration["spec"]["externalMembers"] if m.get("replicaSetName") == shard_rs_name])
    for i in range(n_shard):
        mdb_migration["spec"]["mongodsPerShardCount"] = i + 1
        override = next(o for o in mdb_migration["spec"]["shardOverrides"] if shard_k8s_name in o["shardNames"])
        if not isinstance(override.get("memberConfig"), list):
            override["memberConfig"] = []
        _set_member_config(override["memberConfig"], i, votes=0, priority="0")
        mdb_migration.update()
        wait_until_migrating_condition_reason(mdb_migration, MIGRATING_CONDITION_REASON_EXTENDING, timeout=timeout)
        mdb_migration.assert_reaches_phase(Phase.Running, timeout=timeout)

        pruned += 1
        _remove_one_external(mdb_migration, lambda m: m.get("replicaSetName") == shard_rs_name)
        mdb_migration.update()
        _wait_prune_or_complete(mdb_migration, pruned == total_vms, timeout)

        override = next(o for o in mdb_migration["spec"]["shardOverrides"] if shard_k8s_name in o["shardNames"])
        _set_member_config(override["memberConfig"], i, votes=1, priority="1")
        mdb_migration.update()
        wait_until_phase_and_migrating_condition_reason(
            mdb_migration, "Running", MIGRATING_CONDITION_REASON_IN_PROGRESS, timeout=timeout
        )
        om_tester.assert_cluster_available(mongos_cluster_name)


def extend_and_prune_sharded_mongos(
    mdb_migration: MongoDB,
    om_tester: OMTester,
    mongos_cluster_name: str,
    total_vms: int,
    pruned_so_far: int,
    timeout: int = 600,
) -> None:
    """Incrementally migrate the (stateless) mongos routers: grow mongosCount 0 -> n_mongos, pruning
    one VM mongos per step. Mongos have no votes, so there is no promote step. The last VM pruned is
    the last external member overall -> MigrationComplete. Connectivity is asserted per step by
    re-reading the connection-string secret

    Each prune asserts Running + Pruning only. Do not add a follow-up assertion that the reason
    settles to InProgress: the reason is recomputed solely on reconcile, and a prune that leaves
    external members behind is the last thing that happens to the resource until the next spec
    change, so Pruning legitimately stays put. Convergence is asserted where it matters, on the
    final prune, via MigrationComplete.
    """
    try_load(mdb_migration)
    pruned = pruned_so_far
    n_mongos = len([m for m in mdb_migration["spec"]["externalMembers"] if m["type"] == "mongos"])
    for i in range(n_mongos):
        mdb_migration["spec"]["mongosCount"] = i + 1
        mdb_migration.update()
        wait_until_migrating_condition_reason(mdb_migration, MIGRATING_CONDITION_REASON_EXTENDING, timeout=timeout)
        mdb_migration.assert_reaches_phase(Phase.Running, timeout=timeout)

        pruned += 1
        is_last = pruned == total_vms
        _remove_one_external(mdb_migration, lambda m: m["type"] == "mongos")
        mdb_migration.update()
        _wait_prune_or_complete(mdb_migration, is_last, timeout)

        om_tester.assert_cluster_available(mongos_cluster_name)
        sharded_connection_string_tester(mdb_migration).assert_connectivity()


def external_domain_of_tier(mdb_migration: MongoDB, tier: str) -> Optional[str]:
    """External domain in force for one tier, mirroring the operator's resolution order.

    Per-tier spec.<tier>.externalAccess first, then top-level spec.externalAccess. The operator's
    MongoDbSpec.EffectiveExternalAccessConfiguration only falls back to the top level for mongos in
    single-cluster, but reading it for every tier here is harmless: these tests set the per-tier field
    on all three tiers, so the fallback is never reached for configSrv or shard.
    """
    spec = mdb_migration["spec"]
    per_tier = spec.get(tier, {}).get("externalAccess", {}).get("externalDomain")
    if per_tier:
        return per_tier
    return spec.get("externalAccess", {}).get("externalDomain")


def k8s_mongos_hostnames(mdb_migration: MongoDB) -> list[str]:
    """Hostnames and ports the operator publishes for the Kubernetes-hosted mongos members."""
    external_domain = external_domain_of_tier(mdb_migration, "mongos")
    mongos_count = mdb_migration["spec"]["mongosCount"]

    if external_domain:
        return [f"{mdb_migration.name}-mongos-{i}.{external_domain}:27017" for i in range(mongos_count)]

    service_name = f"{mdb_migration.name}-svc"
    return [
        f"{mdb_migration.name}-mongos-{i}.{service_name}.{mdb_migration.namespace}.svc.cluster.local:27017"
        for i in range(mongos_count)
    ]


def k8s_tier_hostnames(mdb_migration: MongoDB, tier: str, shard_idx: int = 0) -> list[str]:
    """Hostnames (no port) the operator publishes for one tier's Kubernetes-hosted members.

    StatefulSet name prefixes follow the sharded controller: <name>-config for config servers,
    <name>-<shardIdx> for shards, <name>-mongos for mongos. shard_idx selects which shard's
    replica set is addressed; it is ignored for the configSrv and mongos tiers.
    """
    external_domain = external_domain_of_tier(mdb_migration, tier)
    spec = mdb_migration["spec"]

    if tier == "configSrv":
        prefix, count = f"{mdb_migration.name}-config", spec["configServerCount"]
    elif tier == "mongos":
        prefix, count = f"{mdb_migration.name}-mongos", spec["mongosCount"]
    else:
        prefix, count = f"{mdb_migration.name}-{shard_idx}", spec["mongodsPerShardCount"]

    if external_domain:
        return [f"{prefix}-{i}.{external_domain}" for i in range(count)]

    service_suffix = f"{mdb_migration.namespace}.svc.cluster.local"
    internal_service = {"configSrv": "-cs", "mongos": "-svc", "shard": "-sh"}[tier]
    return [f"{prefix}-{i}.{mdb_migration.name}{internal_service}.{service_suffix}" for i in range(count)]


def external_mongos_tester(mdb_migration: MongoDB) -> MongoTester:
    """MongoTester connecting to mongos through the external domain rather than internal service DNS.

    Connecting through internal DNS here would prove nothing: internal DNS still resolves in the test
    cluster, so such a test would pass even if externalAccess were misconfigured.
    """
    hosts = ",".join(k8s_mongos_hostnames(mdb_migration))
    return MongoTester(f"mongodb://{hosts}/", use_ssl=False)


def sharded_migration_connection_strings(mdb_migration: MongoDB) -> tuple[str, str]:
    secret = KubernetesTester.read_secret(mdb_migration.namespace, cluster_connection_string_secret_name(mdb_migration))
    return secret.get("connectionString.standard", ""), secret.get("connectionString.standardSrv", "")


def assert_sharded_connection_string_uses_external_domain(mdb_migration: MongoDB, fully_migrated: bool = False) -> None:
    """Both connection strings address the Kubernetes mongos members through the external domain.

    For a sharded cluster both strings are built from the mongos tier: BuildConnectionString uses
    MongosRsName() and Spec.Replicas() returns MongosCount. Before pruning, the VM members' internal
    hostnames are legitimately still present in the standard string, so the "no internal hostnames"
    check only applies once fully migrated.
    """
    external_domain = external_domain_of_tier(mdb_migration, "mongos")
    assert external_domain, "resource has no mongos externalAccess.externalDomain configured"

    conn_str, conn_srv = sharded_migration_connection_strings(mdb_migration)

    for hostname in k8s_mongos_hostnames(mdb_migration):
        assert hostname in conn_str, f"external mongos hostname {hostname!r} missing from standard connection string"

    assert (
        f"mongodb+srv://{external_domain}/" in conn_srv
    ), f"SRV connection string must use the external domain, got: {conn_srv}"
    assert (
        ".svc.cluster.local" not in conn_srv
    ), f"SRV connection string must not contain an internal service FQDN, got: {conn_srv}"

    if fully_migrated:
        assert (
            ".svc.cluster.local" not in conn_str
        ), f"standard connection string must not contain internal hostnames, got: {conn_str}"


def assert_connection_string_after_full_sharded_migration(
    mdb_migration: MongoDB, ca_path: str | None = None, external: bool = False
) -> None:
    """After all external members are pruned, assert the sharded cluster is reachable.

    Pass ca_path for TLS-enforced clusters so the connectivity check uses a TLS client;
    without it a plaintext client can never connect and assert_connectivity times out.
    Pass external=True when the resource is addressed through an external domain: mdb_migration.tester()
    builds an internal URI, which would pass even if externalAccess were broken.
    """
    assert not mdb_migration["spec"].get("externalMembers"), "expected all external members to be pruned by now"

    if external:
        external_mongos_tester(mdb_migration).assert_connectivity()
        return

    mdb_migration.tester(use_ssl=ca_path is not None, ca_path=ca_path).assert_connectivity()


def assert_common_generated_sharded_cr_shape(
    generated_cr: dict,
    expected_config_count: int,
    expected_shard_count: int,
    expected_mongos_count: int,
    version_id: str,
) -> None:
    """Assert the generated sharded CR has the expected externalMembers and dry-run annotation."""
    assert generated_cr.get("kind") == "MongoDB", f"Expected kind=MongoDB, got: {generated_cr.get('kind')}"

    spec = generated_cr.get("spec", {})
    assert "externalMembers" in spec, "externalMembers missing from generated CR"

    external_members = spec["externalMembers"]
    expected_total = expected_config_count + expected_shard_count + expected_mongos_count
    assert (
        len(external_members) == expected_total
    ), f"Expected {expected_total} externalMembers, got {len(external_members)}"
    for m in external_members:
        for key in ("processName", "hostname", "type"):
            assert key in m, f"externalMember missing key '{key}': {m}"
        assert m["type"] in ("mongod", "mongos"), f"Unexpected type in externalMember: {m['type']}"
        if m["type"] == "mongod":
            assert "replicaSetName" in m, f"externalMember of type mongod missing 'replicaSetName': {m}"

    annotations = generated_cr.get("metadata", {}).get("annotations", {})
    assert MIGRATION_DRY_RUN_ANNOTATION in annotations, "dry-run annotation missing from generated CR"
    assert_migration_tool_version_annotation(generated_cr, version_id)


def assert_k8s_sharded_process_names(om_tester: OMTester, mdb_migration: MongoDB) -> None:
    """Assert all K8s sharded cluster process names appear in the automation config."""
    ac_tester = om_tester.get_automation_config_tester()
    process_names = [p["name"] for p in ac_tester.get_all_processes()]
    name = mdb_migration.name
    ns = mdb_migration.namespace
    for i in range(mdb_migration["spec"].get("configServerCount", MIN_K8S_CONFIGSRV)):
        assert f"k8s/{ns}/{name}-config-{i}" in process_names
    shard_count = mdb_migration["spec"]["shardCount"]
    mongods_per_shard = mdb_migration["spec"].get("mongodsPerShardCount", MIN_K8S_SHARD)
    for shard in range(shard_count):
        for i in range(mongods_per_shard):
            assert f"k8s/{ns}/{name}-{shard}-{i}" in process_names
    for i in range(mdb_migration["spec"].get("mongosCount", MIN_K8S_MONGOS)):
        assert f"k8s/{ns}/{name}-mongos-{i}" in process_names


def vm_mongos_tester(
    mongos_sts_name: str, mongos_svc_name: str, namespace: str, ca_path: str | None = None
) -> MongoTester:
    """Return a MongoTester pointed at the first VM mongos pod."""
    uri = build_mongodb_connection_uri(
        mdb_resource=mongos_sts_name,
        namespace=namespace,
        members=1,
        port="27017",
        servicename=mongos_svc_name,
    )
    return MongoTester(uri, use_ssl=ca_path is not None, ca_path=ca_path)


def build_sharded_cluster_ac(
    om_tester: OMTester,
    configsrv_sts_name: str,
    shard_sts_name: str,
    mongos_sts_name: str,
    configsrv_service_name: str,
    shard_service_name: str,
    mongos_service_name: str,
    namespace: str,
    mongodb_version: str,
    config_rs_name: str,
    shard_rs_name: str,
    config_server_count: int = MIN_VM_CONFIGSRV,
    shard_count: int = MIN_VM_SHARD,
    mongos_count: int = MIN_VM_MONGOS,
    cluster_name: Optional[str] = None,
    tls: bool = False,
    mongod_cert_path: str = "/mongodb-automation/server.pem",
    mongos_cert_path: str = "/mongodb-automation/server.pem",
    ca_cert_path: str = "/mongodb-automation/tls/ca/ca-pem",
    agent_cert_path: str = "",
    x509_agent_subject_dn: str = "",
    compressors: Optional[str] = None,
    directory_per_db: bool = False,
) -> dict:
    """Build an automation config dict for a pseudo-VM sharded cluster.

    Returns an AC with processes, replicaSets, and sharding entries. Does
    not PUT the config. Each process has net.tls.mode set to "disabled"
    unless tls=True, in which case requireTLS is used with the provided cert paths.

    The config server replica set uses pods 0..(config_server_count-1) from the
    config server StatefulSet. The shard replica set uses pods
    0..(shard_count-1) from the shard StatefulSet.
    """
    if cluster_name is None:
        cluster_name = config_rs_name[: -len("-config")] if config_rs_name.endswith("-config") else config_rs_name

    ac = om_tester.api_get_automation_config()
    ac["processes"] = []
    ac["replicaSets"] = []
    ac["sharding"] = []
    ac["monitoringVersions"] = []
    ac["backupVersions"] = []

    def _fqdn(sts: str, pod_index: int, svc: str) -> str:
        return f"{sts}-{pod_index}.{svc}.{namespace}.svc.cluster.local"

    def _monitoring_entry(hostname: str) -> dict:
        entry = {
            "hostname": hostname,
            "logPath": "/var/log/mongodb-mms-automation/monitoring-agent.log",
            "logRotate": {"sizeThresholdMB": 1000, "timeThresholdHrs": 24},
        }
        if tls:
            entry["additionalParams"] = {
                "sslTrustedServerCertificates": ca_cert_path,
                "useSslForAllConnections": "true",
            }
        return entry

    def _backup_entry(hostname: str) -> dict:
        return {
            "hostname": hostname,
            "logPath": "/var/log/mongodb-mms-automation/backup-agent.log",
            "logRotate": {"sizeThresholdMB": 1000, "timeThresholdHrs": 24},
        }

    def _mongod_process(sts_name: str, svc_name: str, pod_index: int, rs_name: str) -> dict:
        hostname = _fqdn(sts_name, pod_index, svc_name)
        process_name = f"{sts_name}-{pod_index}"
        net = {"port": 27017}
        if tls:
            net["tls"] = {"mode": "requireTLS", "certificateKeyFile": mongod_cert_path}
        else:
            net["tls"] = {"mode": "disabled"}
        if compressors:
            net["compression"] = {"compressors": compressors}
        storage = {"dbPath": "/data/"}
        if directory_per_db:
            storage["directoryPerDB"] = True
        return {
            "version": mongodb_version,
            "name": process_name,
            "hostname": hostname,
            "logRotate": {"sizeThresholdMB": 1000, "timeThresholdHrs": 24},
            "authSchemaVersion": 5,
            "featureCompatibilityVersion": fcv_from_version(mongodb_version),
            "processType": "mongod",
            "args2_6": {
                "net": net,
                "storage": storage,
                "systemLog": {"path": "/data/mongodb.log", "destination": "file"},
                "replication": {"replSetName": rs_name},
            },
        }

    config_rs_members = []
    for i in range(config_server_count):
        config_rs_members.append(
            {
                "_id": i,
                "host": f"{configsrv_sts_name}-{i}",
                "priority": 1,
                "votes": 1,
                "secondaryDelaySecs": 0,
                "hidden": False,
                "arbiterOnly": False,
            }
        )
        process = _mongod_process(configsrv_sts_name, configsrv_service_name, i, config_rs_name)
        process["args2_6"]["sharding"] = {"clusterRole": "configsvr"}
        ac["processes"].append(process)
        hostname = _fqdn(configsrv_sts_name, i, configsrv_service_name)
        ac["monitoringVersions"].append(_monitoring_entry(hostname))
        ac["backupVersions"].append(_backup_entry(hostname))

    ac["replicaSets"].append({"_id": config_rs_name, "members": config_rs_members, "protocolVersion": "1"})

    shard_rs_members = []
    for j in range(shard_count):
        shard_rs_members.append(
            {
                "_id": j,
                "host": f"{shard_sts_name}-{j}",
                "priority": 1,
                "votes": 1,
                "secondaryDelaySecs": 0,
                "hidden": False,
                "arbiterOnly": False,
            }
        )
        process = _mongod_process(shard_sts_name, shard_service_name, j, shard_rs_name)
        process["args2_6"]["sharding"] = {"clusterRole": "shardsvr"}
        ac["processes"].append(process)
        hostname = _fqdn(shard_sts_name, j, shard_service_name)
        ac["monitoringVersions"].append(_monitoring_entry(hostname))
        ac["backupVersions"].append(_backup_entry(hostname))

    ac["replicaSets"].append({"_id": shard_rs_name, "members": shard_rs_members, "protocolVersion": "1"})

    for k in range(mongos_count):
        hostname = _fqdn(mongos_sts_name, k, mongos_service_name)
        process_name = f"{mongos_sts_name}-{k}"
        mongos_net = {"port": 27017}
        if tls:
            mongos_net["tls"] = {"mode": "requireTLS", "certificateKeyFile": mongos_cert_path}
        else:
            mongos_net["tls"] = {"mode": "disabled"}
        ac["processes"].append(
            {
                "version": mongodb_version,
                "name": process_name,
                "hostname": hostname,
                "logRotate": {"sizeThresholdMB": 1000, "timeThresholdHrs": 24},
                "authSchemaVersion": 5,
                "featureCompatibilityVersion": fcv_from_version(mongodb_version),
                "processType": "mongos",
                "args2_6": {
                    "net": mongos_net,
                    "systemLog": {"path": "/data/mongodb.log", "destination": "file"},
                },
                "cluster": cluster_name,
            }
        )
        ac["monitoringVersions"].append(_monitoring_entry(hostname))
        ac["backupVersions"].append(_backup_entry(hostname))

    if tls:
        tls_block: dict = {
            "CAFilePath": ca_cert_path,
            "clientCertificateMode": "REQUIRE" if x509_agent_subject_dn else "OPTIONAL",
        }
        if agent_cert_path:
            tls_block["autoPEMKeyFilePath"] = agent_cert_path
        ac["tls"] = tls_block

    if x509_agent_subject_dn:
        ac["auth"] = {
            "disabled": False,
            "authoritativeSet": True,
            "autoUser": x509_agent_subject_dn,
            "autoAuthMechanism": "MONGODB-X509",
            "autoAuthMechanisms": ["MONGODB-X509"],
            "autoAuthRestrictions": [],
            "deploymentAuthMechanisms": ["MONGODB-X509"],
            "keyfile": "/var/lib/mongodb-mms-automation/keyfile",
            "keyfileWindows": "%SystemDrive%\\MMSAutomation\\versions\\keyfile",
            "key": "dGVzdC1rZXlmaWxlLWNvbnRlbnQtZm9yLXZtLW1pZ3JhdGlvbi14NTA5",
            "usersWanted": [],
            "usersDeleted": [],
        }

    ac["sharding"].append(
        {
            "name": cluster_name,
            "configServerReplica": config_rs_name,
            "shards": [{"_id": shard_rs_name, "rs": shard_rs_name, "tags": []}],
            "managedSharding": True,
        }
    )

    return ac


def promote_and_prune_shard(
    mdb_migration: MongoDB,
    om_tester: OMTester,
    vm_shard_rs_name: str,
    mongos_cluster_name: str,
    timeout: int = 600,
) -> None:
    """Promote each Kubernetes shard member and prune one VM shard member at a time.

    Shard member votes live in the shard's own shardOverride, so they must be promoted explicitly
    here. Otherwise pruning the voting VM members would leave the shard with only non voting
    Kubernetes members, which is not a valid replica set.
    """
    try_load(mdb_migration)
    shard_k8s_name = _shard_k8s_name_for_rs(mdb_migration, vm_shard_rs_name)
    vm_shard_count = len(
        [m for m in mdb_migration["spec"]["externalMembers"] if m.get("replicaSetName") == vm_shard_rs_name]
    )
    for i in range(vm_shard_count):
        override = next(o for o in mdb_migration["spec"]["shardOverrides"] if shard_k8s_name in o["shardNames"])
        override["memberConfig"][i]["priority"] = "1"
        override["memberConfig"][i]["votes"] = 1
        mdb_migration.update()
        mdb_migration.assert_reaches_phase(Phase.Running, timeout=timeout)

        current = [m for m in mdb_migration["spec"]["externalMembers"] if m.get("replicaSetName") == vm_shard_rs_name]
        if current:
            mdb_migration["spec"]["externalMembers"].remove(current[-1])
            mdb_migration.update()
            mdb_migration.assert_reaches_phase(Phase.Running, timeout=timeout)

        om_tester.assert_cluster_available(mongos_cluster_name)


def _shard_k8s_name_for_rs(mdb_migration: MongoDB, vm_shard_rs_name: str) -> str:
    """Return the Kubernetes shard name whose AC replica set name is vm_shard_rs_name.

    The AC name comes from shardNameOverrides and falls back to the Kubernetes name.
    """
    spec = mdb_migration["spec"]
    ac_names = {o["shardName"]: o.get("replicaSetName") for o in spec.get("shardNameOverrides", [])}
    for shard_index in range(spec["shardCount"]):
        k8s_name = f"{mdb_migration.name}-{shard_index}"
        if (ac_names.get(k8s_name) or k8s_name) == vm_shard_rs_name:
            return k8s_name
    raise AssertionError(f"no shard maps to replica set {vm_shard_rs_name}")
