from kubetester import try_load
from kubetester.kubetester import KubernetesTester, ensure_ent_version
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB, Phase
from kubetester.operator import Operator
from pytest import fixture, mark
from tests.conftest import (
    OPERATOR_NAME,
    assert_log_rotation_backup_monitoring,
    assert_log_rotation_process,
    is_multi_cluster,
    setup_log_rotate_for_agents,
)
from tests.shardedcluster.conftest import enable_multi_cluster_deployment


@fixture(scope="function")
def sc(namespace: str, custom_mdb_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("sharded-cluster-mongod-options.yaml"),
        namespace=namespace,
    )

    if try_load(resource):
        return resource

    resource.set_version(ensure_ent_version(custom_mdb_version))
    resource.set_architecture_annotation()

    setup_log_rotate_for_agents(resource)

    if is_multi_cluster():
        enable_multi_cluster_deployment(
            resource=resource,
            shard_members_array=[1, 1, 1],
            mongos_members_array=[1, 1],
            configsrv_members_array=[1],
        )

    return resource.update()


@mark.e2e_sharded_cluster_mongod_options_and_log_rotation
def test_install_operator(operator: Operator):
    operator.assert_is_running()


@mark.e2e_sharded_cluster_mongod_options_and_log_rotation
def test_sharded_cluster_created(sc: MongoDB):
    sc.assert_reaches_phase(Phase.Running, timeout=1000)


@mark.e2e_sharded_cluster_mongod_options_and_log_rotation
def test_sharded_cluster_mongodb_options_mongos(sc: MongoDB):
    automation_config_tester = sc.get_automation_config_tester()
    for process in automation_config_tester.get_mongos_processes():
        assert process["args2_6"]["systemLog"]["verbosity"] == 4
        assert process["args2_6"]["systemLog"]["logAppend"]
        assert "operationProfiling" not in process["args2_6"]
        assert "storage" not in process["args2_6"]
        assert process["args2_6"]["net"]["port"] == 30003


@mark.e2e_sharded_cluster_mongod_options_and_log_rotation
def test_sharded_cluster_mongodb_options_config_srv(sc: MongoDB):
    automation_config_tester = sc.get_automation_config_tester()
    config_srv_replicaset_name = sc.config_srv_replicaset_name()
    for process in automation_config_tester.get_replica_set_processes(config_srv_replicaset_name):
        assert process["args2_6"]["operationProfiling"]["mode"] == "slowOp"
        assert "verbosity" not in process["args2_6"]["systemLog"]
        assert "logAppend" not in process["args2_6"]["systemLog"]
        assert "journal" not in process["args2_6"]["storage"]
        assert process["args2_6"]["net"]["port"] == 30002


@mark.e2e_sharded_cluster_mongod_options_and_log_rotation
def test_sharded_cluster_mongodb_options_shards(sc: MongoDB):
    automation_config_tester = sc.get_automation_config_tester()
    for shard_replicaset_name in sc.shard_replicaset_names():
        for process in automation_config_tester.get_replica_set_processes(shard_replicaset_name):
            assert process["args2_6"]["storage"]["journal"]["commitIntervalMs"] == 50
            assert "verbosity" not in process["args2_6"]["systemLog"]
            assert "logAppend" not in process["args2_6"]["systemLog"]
            assert "operationProfiling" not in process["args2_6"]
            assert process["args2_6"]["net"]["port"] == 30001


@mark.e2e_sharded_cluster_mongod_options_and_log_rotation
def test_sharded_cluster_feature_controls(sc: MongoDB):
    fc = sc.get_om_tester().get_feature_controls()
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
        "storage.journal.commitIntervalMs",
        "systemLog.logAppend",
        "systemLog.verbosity",
    ]


@mark.e2e_sharded_cluster_mongod_options_and_log_rotation
def test_remove_fields(sc: MongoDB):
    # delete a field from each component
    sc["spec"]["mongos"]["additionalMongodConfig"]["systemLog"]["verbosity"] = None
    sc["spec"]["shard"]["additionalMongodConfig"]["storage"]["journal"]["commitIntervalMs"] = None
    sc["spec"]["configSrv"]["additionalMongodConfig"]["operationProfiling"]["mode"] = None

    sc.update()

    sc.assert_reaches_phase(Phase.Running, timeout=1000)


@mark.e2e_sharded_cluster_mongod_options_and_log_rotation
def test_fields_are_successfully_removed_from_mongos(sc: MongoDB):
    automation_config_tester = sc.get_automation_config_tester()
    for process in automation_config_tester.get_mongos_processes():
        assert "verbosity" not in process["args2_6"]["systemLog"]

        # other fields are still there
        assert process["args2_6"]["systemLog"]["logAppend"]
        assert "operationProfiling" not in process["args2_6"]
        assert "storage" not in process["args2_6"]
        assert process["args2_6"]["net"]["port"] == 30003


@mark.e2e_sharded_cluster_mongod_options_and_log_rotation
def test_fields_are_successfully_removed_from_config_srv(sc: MongoDB):
    automation_config_tester = sc.get_automation_config_tester()
    config_srv_replicaset_name = sc.config_srv_replicaset_name()
    for process in automation_config_tester.get_replica_set_processes(config_srv_replicaset_name):
        assert "mode" not in process["args2_6"]["operationProfiling"]

        # other fields are still there
        assert "verbosity" not in process["args2_6"]["systemLog"]
        assert "logAppend" not in process["args2_6"]["systemLog"]
        assert "journal" not in process["args2_6"]["storage"]
        assert process["args2_6"]["net"]["port"] == 30002


@mark.e2e_sharded_cluster_mongod_options_and_log_rotation
def test_fields_are_successfully_removed_from_shards(sc: MongoDB):
    automation_config_tester = sc.get_automation_config_tester()
    for shard_replicaset_name in sc.shard_replicaset_names():
        for process in automation_config_tester.get_replica_set_processes(shard_replicaset_name):
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
