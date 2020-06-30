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


@mark.e2e_sharded_cluster_mongod_options
def test_sharded_cluster_mongodb_options_shards(sharded_cluster: MongoDB):
    automation_config_tester = sharded_cluster.get_automation_config_tester()
    for shard_name in sharded_cluster.shards_statefulsets_names():
        for process in automation_config_tester.get_replica_set_processes(shard_name):
            assert process["args2_6"]["storage"]["journal"]["commitIntervalMs"] == 50
            assert "verbosity" not in process["args2_6"]["systemLog"]
            assert "logAppend" not in process["args2_6"]["systemLog"]
            assert "operationProfiling" not in process["args2_6"]
