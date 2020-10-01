from pytest import mark, fixture

from kubetester import find_fixture

from kubetester.opsmanager import MongoDBOpsManager
from kubetester.mongodb import Phase

from kubetester.kubetester import KubernetesTester

from typing import Optional


@fixture(scope="module")
def ops_manager(
    namespace: str, custom_version: Optional[str], custom_appdb_version: str
) -> MongoDBOpsManager:
    resource = MongoDBOpsManager.from_yaml(
        find_fixture("om_validation.yaml"), namespace=namespace, name="om-agent-flags"
    )

    resource["spec"]["applicationDatabase"]["agent"] = {
        "startupOptions": {"logFile": "/var/log/mongodb-mms-automation/customLogFile"}
    }
    resource.set_version(custom_version)
    resource.set_appdb_version(custom_appdb_version)

    return resource.create()


@mark.e2e_om_appdb_agent_flags
def test_appdb(ops_manager: MongoDBOpsManager):
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=400)


@mark.e2e_om_appdb_agent_flags
def test_appdb_has_agent_flags(ops_manager: MongoDBOpsManager):
    cmd = [
        "/bin/sh",
        "-c",
        "ls /var/log/mongodb-mms-automation/customLogFile* | wc -l",
    ]
    for pod in ops_manager.read_appdb_pods():
        result = KubernetesTester.run_command_in_pod_container(
            pod.metadata.name, ops_manager.namespace, cmd,
        )
        assert result != "0"


@mark.e2e_om_appdb_agent_flags
def test_om_get_started(ops_manager: MongoDBOpsManager):
    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=400)
    ops_manager.appdb_status().assert_abandons_phase(Phase.Running, timeout=50)
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=300)


@mark.e2e_om_appdb_agent_flags
def test_appdb_flags_changed(ops_manager: MongoDBOpsManager):
    ops_manager.load()
    ops_manager["spec"]["applicationDatabase"]["agent"]["startupOptions"][
        "dialTimeoutSeconds"
    ] = "70"
    ops_manager.update()
    ops_manager.appdb_status().assert_abandons_phase(Phase.Running, timeout=30)
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=400)


@mark.e2e_om_appdb_agent_flags
def test_appdb_has_changed_agent_flags(ops_manager: MongoDBOpsManager, namespace: str):
    MMS_AUTOMATION_AGENT_PGREP = [
        "/bin/sh",
        "-c",
        "pgrep -f -a /mongodb-automation/files/mongodb-mms-automation-agent",
    ]
    for pod in ops_manager.read_appdb_pods():
        result = KubernetesTester.run_command_in_pod_container(
            pod.metadata.name, namespace, MMS_AUTOMATION_AGENT_PGREP,
        )
        assert "-logFile /var/log/mongodb-mms-automation/customLogFile" in result
        assert "-dialTimeoutSeconds 70" in result
