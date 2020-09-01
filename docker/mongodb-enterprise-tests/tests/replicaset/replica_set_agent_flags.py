from pytest import mark, fixture

from kubetester import find_fixture

from kubetester.mongodb import MongoDB, Phase

from kubetester.kubetester import KubernetesTester


@fixture(scope="module")
def replica_set(namespace: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        find_fixture("replica-set-basic.yaml"), namespace=namespace
    )

    resource["spec"]["agent"] = {"startupOptions": {"maxLogFiles": "30"}}
    resource["spec"]["version"] = "4.0.0"

    return resource.create()


@mark.e2e_replica_set_agent_flags
def test_replica_set(replica_set: MongoDB):
    replica_set.assert_reaches_phase(Phase.Running, timeout=400)


@mark.e2e_replica_set_agent_flags
def test_replica_set_has_agent_flags(replica_set: MongoDB, namespace: str):
    cmd = [
        "/bin/sh",
        "-c",
        "pgrep -f -a /mongodb-automation/files/mongodb-mms-automation-agent",
    ]
    for i in range(3):
        result = KubernetesTester.run_command_in_pod_container(
            f"replica-set-{i}", namespace, cmd,
        )
        assert " -maxLogFiles 30" in result
