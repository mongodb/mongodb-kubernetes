from pytest import mark, fixture

from kubetester import find_fixture

from kubetester.mongodb import MongoDB, Phase

from kubetester.kubetester import KubernetesTester


@fixture(scope="module")
def sharded_cluster(namespace: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        find_fixture("sharded-cluster.yaml"), namespace=namespace
    )

    resource["spec"]["configSrv"] = {
        "agent": {"startupOptions": {"fooKeySrv": "fooValSrv"}}
    }
    resource["spec"]["mongos"] = {
        "agent": {"startupOptions": {"fooKeyMongos": "fooValMongos"}}
    }
    resource["spec"]["shard"] = {
        "agent": {"startupOptions": {"fooKeyShard": "fooValShard"}}
    }

    return resource.create()


@mark.e2e_sharded_cluster_agent_flags
def test_sharded_cluster(sharded_cluster: MongoDB):
    sharded_cluster.assert_reaches_phase(Phase.Running, timeout=400)


@mark.e2e_sharded_cluster_agent_flags
def test_sharded_cluster_has_agent_flags(sharded_cluster: MongoDB, namespace: str):
    cmd = [
        "/bin/sh",
        "-c",
        "pgrep -f -a /mongodb-automation/files/mongodb-mms-automation-agent",
    ]
    for i in range(3):
        result = KubernetesTester.run_command_in_pod_container(
            f"sh001-base-0-{i}", namespace, cmd,
        )
        assert " -fooKeyShard fooValShard" in result
    for i in range(3):
        result = KubernetesTester.run_command_in_pod_container(
            f"sh001-base-config-{i}", namespace, cmd,
        )
        assert " -fooKeySrv fooValSrv" in result
    for i in range(2):
        result = KubernetesTester.run_command_in_pod_container(
            f"sh001-base-mongos-{i}", namespace, cmd,
        )
        assert " -fooKeyMongos fooValMongos" in result
