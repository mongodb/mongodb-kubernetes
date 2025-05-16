from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB, Phase
from pytest import fixture, mark
from tests.conftest import OPERATOR_NAME


@fixture(scope="module")
def replica_set(namespace: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("replica-set-mongod-options.yaml"),
        namespace=namespace,
    )
    resource["spec"]["persistent"] = True
    return resource.update()


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
        assert process["args2_6"]["net"]["port"] == 30000


@mark.e2e_replica_set_mongod_options
def test_replica_set_feature_controls(replica_set: MongoDB):
    fc = replica_set.get_om_tester().get_feature_controls()
    assert fc["externalManagementSystem"]["name"] == OPERATOR_NAME

    assert len(fc["policies"]) == 3
    # unfortunately OM uses a HashSet for policies...
    policies = sorted(fc["policies"], key=lambda policy: policy["policy"])
    assert policies[0]["policy"] == "DISABLE_SET_MONGOD_CONFIG"
    assert policies[1]["policy"] == "DISABLE_SET_MONGOD_VERSION"
    assert policies[2]["policy"] == "EXTERNALLY_MANAGED_LOCK"

    # OM stores the params into a set - we need to sort to compare
    disabled_params = sorted(policies[0]["disabledParams"])
    assert disabled_params == [
        "net.port",
        "operationProfiling.mode",
        "systemLog.logAppend",
        "systemLog.verbosity",
    ]


@mark.e2e_replica_set_mongod_options
def test_replica_set_updated(replica_set: MongoDB):
    replica_set["spec"]["additionalMongodConfig"]["systemLog"]["verbosity"] = 2
    replica_set["spec"]["additionalMongodConfig"]["net"]["maxIncomingConnections"] = 100

    # update uses json merge+patch which means that deleting keys is done by setting them to None
    replica_set["spec"]["additionalMongodConfig"]["operationProfiling"] = None

    replica_set.update()
    replica_set.assert_reaches_phase(Phase.Running)


@mark.e2e_replica_set_mongod_options
def test_replica_set_mongodb_options_were_updated(replica_set: MongoDB):
    automation_config_tester = replica_set.get_automation_config_tester()
    for process in automation_config_tester.get_replica_set_processes(replica_set.name):
        assert process["args2_6"]["systemLog"]["verbosity"] == 2
        assert process["args2_6"]["systemLog"]["logAppend"]
        assert process["args2_6"]["net"]["maxIncomingConnections"] == 100
        assert process["args2_6"]["net"]["port"] == 30000
        # the mode setting has been removed
        assert "mode" not in process["args2_6"]["operationProfiling"]


@mark.e2e_replica_set_mongod_options
def test_replica_set_feature_controls_were_updated(replica_set: MongoDB):
    fc = replica_set.get_om_tester().get_feature_controls()
    assert fc["externalManagementSystem"]["name"] == OPERATOR_NAME
    assert len(fc["policies"]) == 3
    policies = sorted(fc["policies"], key=lambda policy: policy["policy"])
    assert policies[0]["policy"] == "DISABLE_SET_MONGOD_CONFIG"
    assert policies[1]["policy"] == "DISABLE_SET_MONGOD_VERSION"
    assert policies[2]["policy"] == "EXTERNALLY_MANAGED_LOCK"

    disabled_params = sorted(policies[0]["disabledParams"])
    assert disabled_params == [
        "net.maxIncomingConnections",
        "net.port",
        "systemLog.logAppend",
        "systemLog.verbosity",
    ]
