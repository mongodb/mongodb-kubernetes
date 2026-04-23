"""
e2e test for the spec.shards (named shards) API.

Scenario:
    1. Deploy a sharded cluster via the legacy spec.shardCount API
       (shardCount=2). Load data.
    2. Scale up via shardCount to 3 shards.
    3. Attempt to migrate to spec.shards with a typo (identity-breaking
       names) — expect the operator to surface a Failed phase and the
       validation error.
    4. Fix the migration with identity-preserving names. Verify:
       - all shard StatefulSet names are unchanged
       - StatefulSet .metadata.generation is unchanged (no spec rewrite
         = no pod restart)
       - Ops Manager automation-config version is unchanged
       - the Ops Manager shards list is unchanged
    5. Append a new shard with a completely different (non-index-based)
       name. Verify the new StatefulSet exists and that the automation
       config now lists 4 shards, while the original three are untouched.
    6. Remove one of the original index-based shards. Verify:
       - its StatefulSet is deleted
       - the automation config removed the shard
       - remaining shards still untouched
"""

import kubernetes
import pytest
from kubetester import try_load
from kubetester.kubetester import ensure_ent_version
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.kubetester import is_multi_cluster
from kubetester.mongodb import MongoDB
from kubetester.operator import Operator
from kubetester.phase import Phase
from pytest import fixture, mark
from tests.shardedcluster.conftest import (
    enable_multi_cluster_deployment,
    get_member_cluster_clients_using_cluster_mapping,
    get_mongos_service_names,
)


@fixture(scope="module")
def sc(namespace: str, custom_mdb_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("sharded-cluster-named-shards.yaml"),
        namespace=namespace,
    )
    resource.set_version(ensure_ent_version(custom_mdb_version))
    resource.set_architecture_annotation()
    # persistent=False keeps the test hermetic — we don't reuse PVs across
    # phases, but each StatefulSet still manages its own PVCs.
    resource["spec"]["persistent"] = False

    if is_multi_cluster():
        enable_multi_cluster_deployment(
            resource,
            shard_members_array=[1, 1, 1],
            mongos_members_array=[1, None, None],
            configsrv_members_array=[None, 1, None],
        )

    try_load(resource)
    return resource


def _sts_generations(sc: MongoDB, shard_names: list[str]) -> dict[str, int]:
    """Return {stsName: generation} for each shard StatefulSet.

    StatefulSet .metadata.generation bumps only when .spec changes, so
    equal generations across two points in time mean the shard STS spec
    was not rewritten (no pod restart would occur in a real cluster).
    """
    out: dict[str, int] = {}
    for cluster_member_client in get_member_cluster_clients_using_cluster_mapping(sc.name, sc.namespace):
        for name in shard_names:
            if sc.is_multicluster():
                sts_name = f"{name}-{cluster_member_client.cluster_index}"
            else:
                sts_name = name
            sts = cluster_member_client.read_namespaced_stateful_set(sts_name, sc.namespace)
            out[sts_name] = sts.metadata.generation
    return out


def _automation_config_shard_ids(sc: MongoDB) -> list[str]:
    """Return the list of shard _id values from the current automation config."""
    ac = sc.get_automation_config_tester().automation_config
    assert len(ac["sharding"]) == 1
    return sorted(shard["_id"] for shard in ac["sharding"][0]["shards"])


def _automation_config_version(sc: MongoDB) -> int:
    return sc.get_automation_config_tester().automation_config["version"]


@mark.e2e_sharded_cluster_named_shards
def test_install_operator(operator: Operator):
    operator.assert_is_running()


@mark.e2e_sharded_cluster_named_shards
class TestCreateWithShardCount:
    """Creates a sharded cluster via the legacy spec.shardCount API."""

    def test_sharded_cluster_running(self, sc: MongoDB):
        sc.update()
        sc.assert_reaches_phase(Phase.Running, timeout=1000)

    def test_db_connectable_and_data_loaded(self, sc: MongoDB):
        service_names = get_mongos_service_names(sc)
        mongod_tester = sc.tester(service_names=service_names)
        mongod_tester.shard_collection(f"{sc.name}-{{}}", 2, "type")
        mongod_tester.upload_random_data(20_000)
        mongod_tester.assert_number_of_shards(2)


@mark.e2e_sharded_cluster_named_shards
class TestScaleShardCountUp:
    """Scales shardCount to 3 via the legacy API to establish a non-trivial
    baseline before migration."""

    def test_scale_up(self, sc: MongoDB):
        sc.load()
        sc["spec"]["shardCount"] = 3
        sc.update()
        sc.assert_reaches_phase(Phase.Running, timeout=1200)

    def test_three_shards_visible(self, sc: MongoDB):
        ac_ids = _automation_config_shard_ids(sc)
        assert ac_ids == sorted([f"{sc.name}-0", f"{sc.name}-1", f"{sc.name}-2"])


@mark.e2e_sharded_cluster_named_shards
class TestMigrateWithTypoFails:
    """
    Attempts migration with identity-breaking names. The webhook validator
    `shardIdentityImmutable` rejects any migration from spec.shardCount to
    spec.shards where the new shardNames at positions [0, oldShardCount)
    don't match the previously implicit identity "<mdb-name>-<i>". This
    guards against silent rewrites of existing StatefulSets and replica sets.
    """

    def test_apply_migration_with_typo_is_rejected(self, sc: MongoDB):
        sc.load()
        sc["spec"]["shardCount"] = 0
        sc["spec"]["shards"] = [
            {"shardName": f"{sc.name}-0"},
            {"shardName": f"{sc.name}-one"},  # typo: should be "<name>-1"
            {"shardName": f"{sc.name}-2"},
        ]
        with pytest.raises(kubernetes.client.ApiException) as excinfo:
            sc.update()
        assert excinfo.value.status in (
            400,
            403,
        ), f"expected webhook rejection, got {excinfo.value.status}: {excinfo.value.body}"
        assert "must preserve shard identity" in (excinfo.value.body or "")

    def test_apply_migration_with_reorder_is_rejected(self, sc: MongoDB):
        sc.load()
        sc["spec"]["shardCount"] = 0
        sc["spec"]["shards"] = [
            {"shardName": f"{sc.name}-1"},  # swapped with -0
            {"shardName": f"{sc.name}-0"},
            {"shardName": f"{sc.name}-2"},
        ]
        with pytest.raises(kubernetes.client.ApiException) as excinfo:
            sc.update()
        assert excinfo.value.status in (400, 403)
        assert "must preserve shard identity" in (excinfo.value.body or "")


@mark.e2e_sharded_cluster_named_shards
class TestMigrateIdentityPreserving:
    """
    Fixes the migration with correct identity-preserving names. Verifies
    that after the migration NOTHING changes at the Kubernetes or Ops
    Manager level:
      - shard StatefulSet names are unchanged
      - StatefulSet .metadata.generation is unchanged (no .spec edit)
      - Ops Manager automation-config version is unchanged
      - Ops Manager shard list is unchanged
    """

    expected_shard_names: list[str]
    generations_before: dict[str, int]
    ac_version_before: int
    ac_shards_before: list[str]

    def test_snapshot_before(self, sc: MongoDB):
        sc.load()
        TestMigrateIdentityPreserving.expected_shard_names = [f"{sc.name}-{i}" for i in range(3)]
        TestMigrateIdentityPreserving.generations_before = _sts_generations(
            sc, TestMigrateIdentityPreserving.expected_shard_names
        )
        TestMigrateIdentityPreserving.ac_version_before = _automation_config_version(sc)
        TestMigrateIdentityPreserving.ac_shards_before = _automation_config_shard_ids(sc)

    def test_apply_named_shards(self, sc: MongoDB):
        sc.load()
        sc["spec"]["shardCount"] = 0
        sc["spec"]["shards"] = [{"shardName": name} for name in TestMigrateIdentityPreserving.expected_shard_names]
        sc.update()
        sc.assert_reaches_phase(Phase.Running, timeout=600)

    def test_statefulset_names_unchanged(self, sc: MongoDB):
        for cluster_member_client in get_member_cluster_clients_using_cluster_mapping(sc.name, sc.namespace):
            cluster_idx = cluster_member_client.cluster_index
            for shard_idx in range(3):
                sts_name = sc.shard_statefulset_name(shard_idx, cluster_idx)
                sts = cluster_member_client.read_namespaced_stateful_set(sts_name, sc.namespace)
                assert sts is not None

    def test_statefulset_generation_unchanged(self, sc: MongoDB):
        generations_after = _sts_generations(sc, TestMigrateIdentityPreserving.expected_shard_names)
        assert generations_after == TestMigrateIdentityPreserving.generations_before, (
            "StatefulSet .metadata.generation changed during migration — pods would have restarted. "
            f"before={TestMigrateIdentityPreserving.generations_before} after={generations_after}"
        )

    def test_ops_manager_shard_list_unchanged(self, sc: MongoDB):
        assert _automation_config_shard_ids(sc) == TestMigrateIdentityPreserving.ac_shards_before

    def test_ops_manager_config_version_unchanged(self, sc: MongoDB):
        ac_version_after = _automation_config_version(sc)
        assert ac_version_after == TestMigrateIdentityPreserving.ac_version_before, (
            "Ops Manager automation-config version was bumped during migration — something about "
            f"the published config changed. before={TestMigrateIdentityPreserving.ac_version_before} "
            f"after={ac_version_after}"
        )

    def test_data_still_there(self, sc: MongoDB):
        service_names = get_mongos_service_names(sc)
        mongod_tester = sc.tester(service_names=service_names)
        mongod_tester.assert_data_size(20_000)


@mark.e2e_sharded_cluster_named_shards
class TestAddCustomNamedShard:
    """
    Appends a new shard with a name that does NOT follow the index pattern.
    Verifies that:
      - the new StatefulSet is created with the custom name
      - the automation config lists the new shard
      - the pre-existing three shards are untouched (generations stable)
    """

    custom_shard_name = "extra-shard-alpha"
    generations_before: dict[str, int]
    expected_original_names: list[str]

    def test_snapshot_before(self, sc: MongoDB):
        sc.load()
        TestAddCustomNamedShard.expected_original_names = [f"{sc.name}-{i}" for i in range(3)]
        TestAddCustomNamedShard.generations_before = _sts_generations(
            sc, TestAddCustomNamedShard.expected_original_names
        )

    def test_append_custom_shard(self, sc: MongoDB):
        sc.load()
        sc["spec"]["shards"] = sc["spec"]["shards"] + [{"shardName": TestAddCustomNamedShard.custom_shard_name}]
        sc.update()
        sc.assert_reaches_phase(Phase.Running, timeout=1000)

    def test_new_statefulset_exists(self, sc: MongoDB):
        for cluster_member_client in get_member_cluster_clients_using_cluster_mapping(sc.name, sc.namespace):
            if sc.is_multicluster():
                sts_name = f"{TestAddCustomNamedShard.custom_shard_name}-{cluster_member_client.cluster_index}"
            else:
                sts_name = TestAddCustomNamedShard.custom_shard_name
            sts = cluster_member_client.read_namespaced_stateful_set(sts_name, sc.namespace)
            assert sts is not None

    def test_automation_config_lists_four_shards(self, sc: MongoDB):
        ac_ids = _automation_config_shard_ids(sc)
        expected = sorted(TestAddCustomNamedShard.expected_original_names + [TestAddCustomNamedShard.custom_shard_name])
        assert ac_ids == expected

    def test_original_shards_untouched(self, sc: MongoDB):
        generations_after = _sts_generations(sc, TestAddCustomNamedShard.expected_original_names)
        assert (
            generations_after == TestAddCustomNamedShard.generations_before
        ), "adding a new shard must not rewrite existing shard StatefulSets"


@mark.e2e_sharded_cluster_named_shards
class TestRemoveIndexBasedShard:
    """
    Removes the middle index-based shard (<name>-1). Verifies that:
      - its StatefulSet is deleted
      - the automation config no longer lists it
      - the remaining shards are untouched
    """

    removed_shard: str
    remaining_shards: list[str]

    def test_remove_middle_shard(self, sc: MongoDB):
        sc.load()
        TestRemoveIndexBasedShard.removed_shard = f"{sc.name}-1"
        TestRemoveIndexBasedShard.remaining_shards = [
            f"{sc.name}-0",
            f"{sc.name}-2",
            TestAddCustomNamedShard.custom_shard_name,
        ]
        sc["spec"]["shards"] = [{"shardName": name} for name in TestRemoveIndexBasedShard.remaining_shards]
        sc.update()
        sc.assert_reaches_phase(Phase.Running, timeout=1400)

    def test_removed_shard_statefulset_deleted(self, sc: MongoDB):
        for cluster_member_client in get_member_cluster_clients_using_cluster_mapping(sc.name, sc.namespace):
            if sc.is_multicluster():
                sts_name = f"{TestRemoveIndexBasedShard.removed_shard}-{cluster_member_client.cluster_index}"
            else:
                sts_name = TestRemoveIndexBasedShard.removed_shard
            with pytest.raises(kubernetes.client.ApiException) as api_exception:
                cluster_member_client.read_namespaced_stateful_set(sts_name, sc.namespace)
            assert api_exception.value.status == 404

    def test_remaining_statefulsets_exist(self, sc: MongoDB):
        for cluster_member_client in get_member_cluster_clients_using_cluster_mapping(sc.name, sc.namespace):
            for name in TestRemoveIndexBasedShard.remaining_shards:
                if sc.is_multicluster():
                    sts_name = f"{name}-{cluster_member_client.cluster_index}"
                else:
                    sts_name = name
                sts = cluster_member_client.read_namespaced_stateful_set(sts_name, sc.namespace)
                assert sts is not None

    def test_automation_config_reflects_removal(self, sc: MongoDB):
        ac_ids = _automation_config_shard_ids(sc)
        assert TestRemoveIndexBasedShard.removed_shard not in ac_ids
        assert sorted(TestRemoveIndexBasedShard.remaining_shards) == ac_ids
