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
        "agent": {
            "startupOptions": {
                "logFile": "/var/log/mongodb-mms-automation/customLogFileSrv"
            }
        }
    }
    resource["spec"]["mongos"] = {
        "agent": {
            "startupOptions": {
                "logFile": "/var/log/mongodb-mms-automation/customLogFileMongos"
            }
        }
    }
    resource["spec"]["shard"] = {
        "agent": {
            "startupOptions": {
                "logFile": "/var/log/mongodb-mms-automation/customLogFileShard"
            }
        }
    }

    return resource.create()


@mark.e2e_sharded_cluster_agent_flags
def test_sharded_cluster(sharded_cluster: MongoDB):
    sharded_cluster.assert_reaches_phase(Phase.Running, timeout=400)


@mark.e2e_sharded_cluster_agent_flags
def test_sharded_cluster_has_agent_flags(sharded_cluster: MongoDB, namespace: str):
    for i in range(3):
        cmd = [
            "/bin/sh",
            "-c",
            "ls /var/log/mongodb-mms-automation/customLogFileShard* | wc -l",
        ]
        result = KubernetesTester.run_command_in_pod_container(
            f"sh001-base-0-{i}",
            namespace,
            cmd,
        )
        assert result != "0"
    for i in range(3):
        cmd = [
            "/bin/sh",
            "-c",
            "ls /var/log/mongodb-mms-automation/customLogFileSrv* | wc -l",
        ]
        result = KubernetesTester.run_command_in_pod_container(
            f"sh001-base-config-{i}",
            namespace,
            cmd,
        )
        assert result != "0"
    for i in range(2):
        cmd = [
            "/bin/sh",
            "-c",
            "ls /var/log/mongodb-mms-automation/customLogFileMongos* | wc -l",
        ]
        result = KubernetesTester.run_command_in_pod_container(
            f"sh001-base-mongos-{i}",
            namespace,
            cmd,
        )
        assert result != "0"
