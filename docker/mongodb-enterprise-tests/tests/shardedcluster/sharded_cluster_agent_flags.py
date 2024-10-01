from kubetester import create_or_update, find_fixture, try_load
from kubetester.kubetester import KubernetesTester, ensure_ent_version
from kubetester.mongodb import MongoDB, Phase
from pytest import fixture, mark
from tests.pod_logs import (
    assert_log_types_in_structured_json_pod_log,
    get_all_default_log_types,
    get_all_log_types,
)

NUMBER_OF_SHARDS = 3
NUMBER_OF_CONFIGS = 3
NUMBER_OF_MONGOS = 2

SHARDED_CLUSTER_NAME = "sh001-base"


@fixture(scope="module")
def sharded_cluster(namespace: str, custom_mdb_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(find_fixture("sharded-cluster.yaml"), namespace=namespace)

    if try_load(resource):
        return resource

    resource.set_version(ensure_ent_version(custom_mdb_version))

    resource["spec"]["configSrv"] = {
        "agent": {"startupOptions": {"logFile": "/var/log/mongodb-mms-automation/customLogFileSrv"}}
    }
    resource["spec"]["mongos"] = {
        "agent": {"startupOptions": {"logFile": "/var/log/mongodb-mms-automation/customLogFileMongos"}}
    }
    resource["spec"]["shard"] = {
        "agent": {"startupOptions": {"logFile": "/var/log/mongodb-mms-automation/customLogFileShard"}}
    }

    create_or_update(resource)
    return resource


@mark.e2e_sharded_cluster_agent_flags
def test_sharded_cluster(sharded_cluster: MongoDB):
    sharded_cluster.assert_reaches_phase(Phase.Running, timeout=400)


@mark.e2e_sharded_cluster_agent_flags
def test_sharded_cluster_has_agent_flags(sharded_cluster: MongoDB, namespace: str):
    for i in range(NUMBER_OF_SHARDS):
        cmd = [
            "/bin/sh",
            "-c",
            "ls /var/log/mongodb-mms-automation/customLogFileShard* | wc -l",
        ]
        result = KubernetesTester.run_command_in_pod_container(
            f"{SHARDED_CLUSTER_NAME}-0-{i}",
            namespace,
            cmd,
        )
        assert result != "0"
    for i in range(NUMBER_OF_CONFIGS):
        cmd = [
            "/bin/sh",
            "-c",
            "ls /var/log/mongodb-mms-automation/customLogFileSrv* | wc -l",
        ]
        result = KubernetesTester.run_command_in_pod_container(
            f"{SHARDED_CLUSTER_NAME}-config-{i}",
            namespace,
            cmd,
        )
        assert result != "0"
    for i in range(NUMBER_OF_MONGOS):
        cmd = [
            "/bin/sh",
            "-c",
            "ls /var/log/mongodb-mms-automation/customLogFileMongos* | wc -l",
        ]
        result = KubernetesTester.run_command_in_pod_container(
            f"{SHARDED_CLUSTER_NAME}-mongos-{i}",
            namespace,
            cmd,
        )
        assert result != "0"


@mark.e2e_sharded_cluster_agent_flags
def test_log_types_without_audit_enabled(sharded_cluster: MongoDB):
    assert_log_types_in_pods(sharded_cluster.namespace, get_all_default_log_types())


@mark.e2e_sharded_cluster_agent_flags
def test_enable_audit_log(sharded_cluster: MongoDB):
    additional_mongod_config = {
        "auditLog": {
            "destination": "file",
            "format": "JSON",
            "path": "/var/log/mongodb-mms-automation/mongodb-audit-changed.log",
        }
    }
    sharded_cluster["spec"]["configSrv"]["additionalMongodConfig"] = additional_mongod_config
    sharded_cluster["spec"]["mongos"]["additionalMongodConfig"] = additional_mongod_config
    sharded_cluster["spec"]["shard"]["additionalMongodConfig"] = additional_mongod_config
    create_or_update(sharded_cluster)

    sharded_cluster.assert_reaches_phase(Phase.Running, timeout=1000)


def assert_log_types_in_pods(namespace: str, expected_log_types: set[str]):
    for i in range(NUMBER_OF_SHARDS):
        assert_log_types_in_structured_json_pod_log(namespace, f"{SHARDED_CLUSTER_NAME}-0-{i}", expected_log_types)
    for i in range(NUMBER_OF_CONFIGS):
        assert_log_types_in_structured_json_pod_log(namespace, f"{SHARDED_CLUSTER_NAME}-config-{i}", expected_log_types)
    for i in range(NUMBER_OF_MONGOS):
        assert_log_types_in_structured_json_pod_log(namespace, f"{SHARDED_CLUSTER_NAME}-mongos-{i}", expected_log_types)


@mark.e2e_sharded_cluster_agent_flags
def test_log_types_with_audit_enabled(namespace: str, sharded_cluster: MongoDB):
    assert_log_types_in_pods(sharded_cluster.namespace, get_all_log_types())
