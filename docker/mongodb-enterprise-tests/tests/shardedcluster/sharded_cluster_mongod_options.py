from pytest import fixture, mark

from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB, Phase


@fixture(scope="module")
def sharded_cluster(namespace: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("sharded-cluster-mongod-options.yaml"), namespace=namespace,
    )
    return resource.create()


@mark.e2e_sharded_cluster_mongod_options
def test_sharded_cluster_created(sharded_cluster: MongoDB):
    sharded_cluster.assert_reaches_phase(Phase.Running, timeout=300)


@mark.e2e_sharded_cluster_mongod_options
def test_sharded_cluster_mongodb_options_mongos(sharded_cluster: MongoDB):
    automation_config_tester = sharded_cluster.get_automation_config_tester()
    for process in automation_config_tester.get_mongos_processes():
        assert process["args2_6"]["systemLog"]["verbosity"] == 4
        assert process["args2_6"]["systemLog"]["logAppend"]
        assert "operationProfiling" not in process["args2_6"]
        assert "storage" not in process["args2_6"]
        assert process["args2_6"]["net"]["port"] == 30003


@mark.e2e_sharded_cluster_mongod_options
def test_sharded_cluster_mongodb_options_config_srv(sharded_cluster: MongoDB):
    automation_config_tester = sharded_cluster.get_automation_config_tester()
    for process in automation_config_tester.get_replica_set_processes(
        sharded_cluster.config_srv_statefulset_name()
    ):
        assert process["args2_6"]["operationProfiling"]["mode"] == "slowOp"
        assert "verbosity" not in process["args2_6"]["systemLog"]
        assert "logAppend" not in process["args2_6"]["systemLog"]
        assert "journal" not in process["args2_6"]["storage"]
        assert process["args2_6"]["net"]["port"] == 30002


@mark.e2e_sharded_cluster_mongod_options
def test_sharded_cluster_mongodb_options_shards(sharded_cluster: MongoDB):
    automation_config_tester = sharded_cluster.get_automation_config_tester()
    for shard_name in sharded_cluster.shards_statefulsets_names():
        for process in automation_config_tester.get_replica_set_processes(shard_name):
            assert process["args2_6"]["storage"]["journal"]["commitIntervalMs"] == 50
            assert "verbosity" not in process["args2_6"]["systemLog"]
            assert "logAppend" not in process["args2_6"]["systemLog"]
            assert "operationProfiling" not in process["args2_6"]
            assert process["args2_6"]["net"]["port"] == 30001


@mark.e2e_sharded_cluster_mongod_options
def test_sharded_cluster_feature_controls(sharded_cluster: MongoDB):
    fc = sharded_cluster.get_om_tester().get_feature_controls()
    assert fc["externalManagementSystem"]["name"] == "mongodb-enterprise-operator"

    assert len(fc["policies"]) == 2
    # unfortunately OM uses a HashSet for policies...
    policies = sorted(fc["policies"], key=lambda policy: policy["policy"])
    assert policies[0]["policy"] == "DISABLE_SET_MONGOD_CONFIG"
    assert policies[1]["policy"] == "EXTERNALLY_MANAGED_LOCK"
    # OM stores the params into a set - we need to sort to compare
    disabled_params = sorted(policies[0]["disabledParams"])
    assert disabled_params == [
        "net.port",
        "operationProfiling.mode",
        "storage.journal.commitIntervalMs",
        "systemLog.logAppend",
        "systemLog.verbosity",
    ]
