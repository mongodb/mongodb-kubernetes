from pytest import fixture, mark

from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB, Phase


@fixture(scope="module")
def replica_set(namespace: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("replica-set-mongod-options.yaml"), namespace=namespace,
    )
    return resource.create()


@mark.e2e_replica_set_mongod_options
def test_replica_set_created(replica_set: MongoDB):
    replica_set.assert_reaches_phase(Phase.Running)


@mark.e2e_replica_set_mongod_options
def test_replica_set_mongodb_options(replica_set: MongoDB):
    automation_config_tester = replica_set.get_automation_config_tester()
    for process in automation_config_tester.get_replica_set_processes(replica_set.name):
        assert process["args2_6"]["systemLog"]["verbosity"] == 4
        assert process["args2_6"]["systemLog"]["logAppend"]
        assert process["args2_6"]["operationProfiling"]["mode"] == "slowOp"
        assert process["args2_6"]["net"]["port"] == 27017


@mark.e2e_replica_set_mongod_options
def test_replica_set_updated(replica_set: MongoDB):
    replica_set["spec"]["additionalMongodConfig"]["systemLog"]["verbosity"] = 2
    replica_set["spec"]["additionalMongodConfig"]["net"]["maxIncomingConnections"] = 100
    del replica_set["spec"]["additionalMongodConfig"]["operationProfiling"]
    replica_set.update()
    replica_set.assert_abandons_phase(Phase.Running)
    replica_set.assert_reaches_phase(Phase.Running)


@mark.e2e_replica_set_mongod_options
def test_replica_set_mongodb_options_were_updated(replica_set: MongoDB):
    automation_config_tester = replica_set.get_automation_config_tester()
    for process in automation_config_tester.get_replica_set_processes(replica_set.name):
        assert process["args2_6"]["systemLog"]["verbosity"] == 2
        assert process["args2_6"]["systemLog"]["logAppend"]
        assert process["args2_6"]["net"]["maxIncomingConnections"] == 100
        assert process["args2_6"]["net"]["port"] == 27017
        # operationProfiling is still there - we don't remove the unknown options during merge
        assert process["args2_6"]["operationProfiling"]["mode"] == "slowOp"
