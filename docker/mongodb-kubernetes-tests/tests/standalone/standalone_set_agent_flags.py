from kubetester import find_fixture, try_load
from kubetester.kubetester import KubernetesTester
from kubetester.mongodb import MongoDB
from kubetester.phase import Phase
from pytest import fixture, mark


@fixture(scope="module")
def standalone(namespace: str) -> MongoDB:
    resource = MongoDB.from_yaml(find_fixture("standalone.yaml"), namespace=namespace)

    resource["spec"]["agent"] = {"startupOptions": {"logFile": "/var/log/mongodb-mms-automation/customLogFile"}}

    try_load(resource)
    return resource


@mark.e2e_standalone_agent_flags
def test_standalone(standalone: MongoDB):
    standalone.update()
    standalone.assert_reaches_phase(Phase.Running, timeout=400)


@mark.e2e_standalone_agent_flags
def test_standalone_has_agent_flags(standalone: MongoDB, namespace: str):
    cmd = [
        "/bin/sh",
        "-c",
        "ls /var/log/mongodb-mms-automation/customLogFile* | wc -l",
    ]
    result = KubernetesTester.run_command_in_pod_container(
        "my-standalone-0",
        namespace,
        cmd,
    )
    assert result != "0"
