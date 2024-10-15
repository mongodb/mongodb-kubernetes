from kubernetes import client
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB, Phase
from kubetester.operator import Operator
from pytest import fixture, mark
from tests.conftest import (
    assert_log_rotation_backup_monitoring,
    assert_log_rotation_process,
    setup_log_rotate_for_agents,
)


@fixture(scope="module")
def sharded_cluster(namespace: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("sharded-cluster-mongod-options.yaml"),
        namespace=namespace,
    )

    setup_log_rotate_for_agents(resource)
    resource.update()
    return resource


@mark.e2e_sharded_cluster_mongod_options_and_log_rotation
def test_install_operator(default_operator: Operator):
    default_operator.assert_is_running()


@mark.e2e_sharded_cluster_mongod_options_and_log_rotation
def test_sharded_cluster_created(sharded_cluster: MongoDB):
    sharded_cluster.assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_sharded_cluster_mongod_options_and_log_rotation
def test_sharded_cluster_mongodb_options_mongos(sharded_cluster: MongoDB):
    automation_config_tester = sharded_cluster.get_automation_config_tester()
    for process in automation_config_tester.get_mongos_processes():
        assert process["args2_6"]["systemLog"]["verbosity"] == 4
        assert process["args2_6"]["systemLog"]["logAppend"]
        assert "operationProfiling" not in process["args2_6"]
        assert "storage" not in process["args2_6"]
        assert process["args2_6"]["net"]["port"] == 30003


@mark.e2e_sharded_cluster_mongod_options_and_log_rotation
def test_sharded_cluster_mongodb_options_config_srv(sharded_cluster: MongoDB):
    automation_config_tester = sharded_cluster.get_automation_config_tester()
    for process in automation_config_tester.get_replica_set_processes(sharded_cluster.config_srv_statefulset_name()):
        assert process["args2_6"]["operationProfiling"]["mode"] == "slowOp"
        assert "verbosity" not in process["args2_6"]["systemLog"]
        assert "logAppend" not in process["args2_6"]["systemLog"]
        assert "journal" not in process["args2_6"]["storage"]
        assert process["args2_6"]["net"]["port"] == 30002


@mark.e2e_sharded_cluster_mongod_options_and_log_rotation
def test_sharded_cluster_mongodb_options_shards(sharded_cluster: MongoDB):
    automation_config_tester = sharded_cluster.get_automation_config_tester()
    for shard_name in sharded_cluster.shards_statefulsets_names():
        for process in automation_config_tester.get_replica_set_processes(shard_name):
            assert process["args2_6"]["storage"]["journal"]["commitIntervalMs"] == 50
            assert "verbosity" not in process["args2_6"]["systemLog"]
            assert "logAppend" not in process["args2_6"]["systemLog"]
            assert "operationProfiling" not in process["args2_6"]
            assert process["args2_6"]["net"]["port"] == 30001


@mark.e2e_sharded_cluster_mongod_options_and_log_rotation
def test_sharded_cluster_feature_controls(sharded_cluster: MongoDB):
    fc = sharded_cluster.get_om_tester().get_feature_controls()
    assert fc["externalManagementSystem"]["name"] == "mongodb-enterprise-operator"

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
        "storage.journal.commitIntervalMs",
        "systemLog.logAppend",
        "systemLog.verbosity",
    ]


@mark.e2e_sharded_cluster_mongod_options_and_log_rotation
def test_remove_fields(sharded_cluster: MongoDB):
    sharded_cluster.load()

    # delete a field from each component
    del sharded_cluster["spec"]["mongos"]["additionalMongodConfig"]["systemLog"]["verbosity"]
    del sharded_cluster["spec"]["shard"]["additionalMongodConfig"]["storage"]["journal"]["commitIntervalMs"]
    del sharded_cluster["spec"]["configSrv"]["additionalMongodConfig"]["operationProfiling"]["mode"]

    client.CustomObjectsApi().replace_namespaced_custom_object(
        sharded_cluster.group,
        sharded_cluster.version,
        sharded_cluster.namespace,
        sharded_cluster.plural,
        sharded_cluster.name,
        sharded_cluster.backing_obj,
    )

    sharded_cluster.assert_reaches_phase(Phase.Running)


@mark.e2e_sharded_cluster_mongod_options_and_log_rotation
def test_fields_are_successfully_removed_from_mongos(sharded_cluster: MongoDB):
    automation_config_tester = sharded_cluster.get_automation_config_tester()
    for process in automation_config_tester.get_mongos_processes():
        assert "verbosity" not in process["args2_6"]["systemLog"]

        # other fields are still there
        assert process["args2_6"]["systemLog"]["logAppend"]
        assert "operationProfiling" not in process["args2_6"]
        assert "storage" not in process["args2_6"]
        assert process["args2_6"]["net"]["port"] == 30003


@mark.e2e_sharded_cluster_mongod_options_and_log_rotation
def test_fields_are_successfully_removed_from_config_srv(sharded_cluster: MongoDB):
    automation_config_tester = sharded_cluster.get_automation_config_tester()
    for process in automation_config_tester.get_replica_set_processes(sharded_cluster.config_srv_statefulset_name()):
        assert "mode" not in process["args2_6"]["operationProfiling"]

        # other fields are still there
        assert "verbosity" not in process["args2_6"]["systemLog"]
        assert "logAppend" not in process["args2_6"]["systemLog"]
        assert "journal" not in process["args2_6"]["storage"]
        assert process["args2_6"]["net"]["port"] == 30002


@mark.e2e_sharded_cluster_mongod_options_and_log_rotation
def test_fields_are_successfully_removed_from_shards(sharded_cluster: MongoDB):
    automation_config_tester = sharded_cluster.get_automation_config_tester()
    for shard_name in sharded_cluster.shards_statefulsets_names():
        for process in automation_config_tester.get_replica_set_processes(shard_name):
            assert "commitIntervalMs" not in process["args2_6"]["storage"]["journal"]

            # other fields are still there
            assert "verbosity" not in process["args2_6"]["systemLog"]
            assert "logAppend" not in process["args2_6"]["systemLog"]
            assert "operationProfiling" not in process["args2_6"]
            assert process["args2_6"]["net"]["port"] == 30001


@mark.e2e_sharded_cluster_mongod_options_and_log_rotation
def test_process_log_rotation():
    config = KubernetesTester.get_automation_config()
    for process in config["processes"]:
        assert_log_rotation_process(process)


@mark.e2e_sharded_cluster_mongod_options_and_log_rotation
def test_backup_log_rotation():
    bvk = KubernetesTester.get_backup_config()
    assert_log_rotation_backup_monitoring(bvk)


@mark.e2e_sharded_cluster_mongod_options_and_log_rotation
def test_backup_log_rotation():
    mc = KubernetesTester.get_monitoring_config()
    assert_log_rotation_backup_monitoring(mc)
