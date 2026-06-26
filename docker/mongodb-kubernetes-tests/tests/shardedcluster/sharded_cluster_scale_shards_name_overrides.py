"""Sharded cluster scale up and scale down when shardNameOverrides are set.

Creates a 2-shard cluster using both override forms for shards, plus config server
and mongos name overrides:
  - Shard 0: full form, where shardId and replicaSetName differ from the K8s StatefulSet name.
  - Shard 1: brevity form, where shardName only is set and all three values are equal.
  - Config server: configServerNameOverride sets a custom AC replicaSetName.
  - Mongos: shardedClusterNameOverride sets a custom AC cluster name.
Verifies that:
  - The AC uses the correct names for all four override forms after creation.
  - Scaling down removes the override entry for the scaled-away shard.
  - Scaling up adds a new shard that uses the K8s default name in the AC.
"""

from kubetester import try_load
from kubetester.kubetester import KubernetesTester, ensure_ent_version
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB
from kubetester.omtester import OMContext, OMTester
from kubetester.operator import Operator
from kubetester.phase import Phase
from pytest import fixture, mark

MARK = "e2e_sharded_cluster_scale_shards_name_overrides"

CONFIG_RS_AC_NAME = "ac-config"
MONGOS_AC_NAME = "ac-mongos"
SHARD_0_AC_NAME = "ac-rs-0"
SHARD_1_K8S_NAME = "sc-scale-overrides-1"


@fixture(scope="module")
def om_tester(namespace: str, operator) -> OMTester:
    config_map = KubernetesTester.read_configmap(namespace, "my-project")
    secret = KubernetesTester.read_secret(namespace, "my-credentials")
    tester = OMTester(OMContext.build_from_config_map_and_secret(config_map, secret))
    tester.ensure_agent_api_key()
    return tester


@fixture(scope="module")
def sc(namespace: str, custom_mdb_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("sharded-cluster-scale-shards-name-overrides.yaml"),
        namespace=namespace,
    )
    resource.set_version(ensure_ent_version(custom_mdb_version))
    resource.set_architecture_annotation()
    try_load(resource)
    return resource


@mark.e2e_sharded_cluster_scale_shards_name_overrides
def test_install_operator(operator: Operator):
    operator.assert_is_running()


@mark.e2e_sharded_cluster_scale_shards_name_overrides
class TestCreateWithNameOverrides:
    """
    name: Create sharded cluster with shardNameOverrides
    description: |
      Creates a 2-shard cluster using full form for shard 0 (AC names differ from K8s names)
      and brevity form for shard 1 (AC names equal the K8s StatefulSet name).
      Verifies the AC uses the correct names for each form.
    """

    def test_sharded_cluster_running(self, sc: MongoDB):
        sc.update()
        sc.assert_reaches_phase(Phase.Running, timeout=1000)

    def test_ac_uses_override_names(self, om_tester: OMTester):
        ac_tester = om_tester.get_automation_config_tester()
        ac_tester.assert_sharded_cluster_processes(CONFIG_RS_AC_NAME, [SHARD_0_AC_NAME, SHARD_1_K8S_NAME], 1)

    def test_ac_has_two_shards(self, om_tester: OMTester):
        ac_tester = om_tester.get_automation_config_tester()
        shards = ac_tester.get_sharding_entries()[0]["shards"]
        assert len(shards) == 2
        shard_ids = {s["_id"] for s in shards}
        assert SHARD_0_AC_NAME in shard_ids
        assert SHARD_1_K8S_NAME in shard_ids

    def test_ac_config_and_mongos_use_override_names(self, om_tester: OMTester):
        ac_tester = om_tester.get_automation_config_tester()
        sharding_entry = ac_tester.get_sharding_entries()[0]
        assert sharding_entry["configServerReplica"] == CONFIG_RS_AC_NAME
        assert sharding_entry["name"] == MONGOS_AC_NAME


@mark.e2e_sharded_cluster_scale_shards_name_overrides
class TestScaleDownWithNameOverrides:
    """
    name: Scale down sharded cluster with shardNameOverrides
    description: |
      Scales down to 1 shard and removes the override entry for the scaled-away shard.
      Verifies the AC no longer contains the removed shard's entry.
    """

    def test_scale_down(self, sc: MongoDB):
        sc.load()
        sc["spec"]["shardCount"] = 1
        sc["spec"]["shardNameOverrides"] = [
            {"shardName": "sc-scale-overrides-0", "shardId": SHARD_0_AC_NAME, "replicaSetName": SHARD_0_AC_NAME}
        ]
        sc.update()
        sc.assert_reaches_phase(Phase.Running, timeout=1000)

    def test_ac_shard_1_removed(self, om_tester: OMTester):
        ac_tester = om_tester.get_automation_config_tester()
        ac_tester.assert_sharded_cluster_processes(CONFIG_RS_AC_NAME, [SHARD_0_AC_NAME], 1)

    def test_ac_has_one_shard(self, om_tester: OMTester):
        ac_tester = om_tester.get_automation_config_tester()
        shards = ac_tester.get_sharding_entries()[0]["shards"]
        assert len(shards) == 1
        assert shards[0]["_id"] == SHARD_0_AC_NAME


@mark.e2e_sharded_cluster_scale_shards_name_overrides
class TestScaleUpWithoutNewOverride:
    """
    name: Scale up sharded cluster without adding a new override
    description: |
      Scales back up to 2 shards without adding a shardNameOverrides entry for the new shard.
      Verifies the new shard uses its K8s StatefulSet name in the AC.
    """

    def test_scale_up(self, sc: MongoDB):
        sc.load()
        sc["spec"]["shardCount"] = 2
        sc.update()
        sc.assert_reaches_phase(Phase.Running, timeout=1000)

    def test_ac_new_shard_uses_k8s_name(self, om_tester: OMTester):
        ac_tester = om_tester.get_automation_config_tester()
        ac_tester.assert_sharded_cluster_processes(CONFIG_RS_AC_NAME, [SHARD_0_AC_NAME, SHARD_1_K8S_NAME], 1)

    def test_ac_has_two_shards(self, om_tester: OMTester):
        ac_tester = om_tester.get_automation_config_tester()
        shards = ac_tester.get_sharding_entries()[0]["shards"]
        assert len(shards) == 2
        shard_ids = {s["_id"] for s in shards}
        assert SHARD_0_AC_NAME in shard_ids
        assert SHARD_1_K8S_NAME in shard_ids
