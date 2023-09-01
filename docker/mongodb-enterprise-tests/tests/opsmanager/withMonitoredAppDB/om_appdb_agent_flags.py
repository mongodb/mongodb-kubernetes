from typing import Optional

from pytest import mark, fixture

from kubetester import find_fixture, create_or_update
from kubetester.kubetester import KubernetesTester
from kubetester.mongodb import Phase
from kubetester.opsmanager import MongoDBOpsManager
from tests.conftest import is_multi_cluster
from tests.opsmanager.withMonitoredAppDB.conftest import enable_appdb_multi_cluster_deployment


@fixture(scope="module")
def ops_manager(namespace: str, custom_version: Optional[str], custom_appdb_version: str) -> MongoDBOpsManager:
    resource = MongoDBOpsManager.from_yaml(
        find_fixture("om_validation.yaml"), namespace=namespace, name="om-agent-flags"
    )

    # both monitoring and automation agent should see these changes
    resource["spec"]["applicationDatabase"]["agent"] = {
        "startupOptions": {"logFile": "/var/log/mongodb-mms-automation/customLogFile"}
    }
    resource["spec"]["applicationDatabase"]["memberConfig"] = [
        {
            "votes": 1,
            "priority": "0.5",
            "tags": {
                "tag1": "value1",
                "environment": "prod",
            },
        },
        {
            "votes": 1,
            "priority": "1.5",
            "tags": {
                "tag2": "value2",
                "environment": "prod",
            },
        },
        {
            "votes": 1,
            "priority": "0.5",
            "tags": {
                "tag2": "value2",
                "environment": "prod",
            },
        },
    ]
    resource.set_version(custom_version)
    resource.set_appdb_version(custom_appdb_version)

    if is_multi_cluster():
        enable_appdb_multi_cluster_deployment(resource)

    create_or_update(resource)
    return resource


@mark.e2e_om_appdb_agent_flags
def test_appdb(ops_manager: MongoDBOpsManager):
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_om_appdb_agent_flags
def test_om(ops_manager: MongoDBOpsManager):
    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=800)


@mark.e2e_om_appdb_agent_flags
def test_monitoring_is_configured(ops_manager: MongoDBOpsManager):
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_om_appdb_agent_flags
def test_appdb_has_agent_flags(ops_manager: MongoDBOpsManager):
    cmd = [
        "/bin/sh",
        "-c",
        "ls /var/log/mongodb-mms-automation/customLogFile* | wc -l",
    ]
    for api_client, pod in ops_manager.read_appdb_pods():
        result = KubernetesTester.run_command_in_pod_container(
            pod.metadata.name, ops_manager.namespace, cmd, container="mongodb-agent", api_client=api_client
        )
        assert result != "0"


@mark.e2e_om_appdb_agent_flags
def test_appdb_monitoring_agent_flags_inherit_automation_agent_flags(
    ops_manager: MongoDBOpsManager,
):
    cmd = [
        "/bin/sh",
        "-c",
        "ls /var/log/mongodb-mms-automation/customLogFileMonitoring* | wc -l",
    ]
    for api_client, pod in ops_manager.read_appdb_pods():
        result = KubernetesTester.run_command_in_pod_container(
            pod.metadata.name,
            ops_manager.namespace,
            cmd,
            container="mongodb-agent-monitoring",
            api_client=api_client,
        )
        assert "No such file or directory" in result

    cmd = [
        "/bin/sh",
        "-c",
        "ls /var/log/mongodb-mms-automation/customLogFile* | wc -l",
    ]
    for api_client, pod in ops_manager.read_appdb_pods():
        result = KubernetesTester.run_command_in_pod_container(
            pod.metadata.name,
            ops_manager.namespace,
            cmd,
            container="mongodb-agent-monitoring",
            api_client=api_client,
        )
        assert result != "0"


@mark.e2e_om_appdb_agent_flags
def test_appdb_flags_changed(ops_manager: MongoDBOpsManager):
    ops_manager.load()
    ops_manager["spec"]["applicationDatabase"]["agent"]["startupOptions"]["dialTimeoutSeconds"] = "70"

    ops_manager["spec"]["applicationDatabase"]["monitoringAgent"] = {
        "startupOptions": {
            "logFile": "/var/log/mongodb-mms-automation/customLogFileMonitoring",
            "dialTimeoutSeconds": "80",
        }
    }
    ops_manager.update()
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_om_appdb_agent_flags
def test_appdb_has_changed_agent_flags(ops_manager: MongoDBOpsManager, namespace: str):
    MMS_AUTOMATION_AGENT_PGREP = [
        "/bin/sh",
        "-c",
        "pgrep -f -a agent/mongodb-agent",
    ]
    for api_client, pod in ops_manager.read_appdb_pods():
        result = KubernetesTester.run_command_in_pod_container(
            pod.metadata.name,
            namespace,
            MMS_AUTOMATION_AGENT_PGREP,
            container="mongodb-agent",
            api_client=api_client,
        )
        assert "-logFile=/var/log/mongodb-mms-automation/customLogFile" in result
        assert "-dialTimeoutSeconds=70" in result

        result = KubernetesTester.run_command_in_pod_container(
            pod.metadata.name,
            namespace,
            MMS_AUTOMATION_AGENT_PGREP,
            container="mongodb-agent-monitoring",
            api_client=api_client,
        )
        assert "-logFile=/var/log/mongodb-mms-automation/customLogFileMonitoring" in result
        assert "-dialTimeoutSeconds=80" in result


@mark.e2e_om_appdb_agent_flags
def test_automation_config_secret_member_options(ops_manager: MongoDBOpsManager, namespace: str):
    members = ops_manager.get_automation_config_tester().get_replica_set_members(ops_manager.app_db_name())

    assert members[0]["votes"] == 1
    assert members[0]["priority"] == 0.5
    assert members[0]["tags"] == {"environment": "prod", "tag1": "value1"}

    assert members[1]["votes"] == 1
    assert members[1]["priority"] == 1.5
    assert members[1]["tags"] == {"environment": "prod", "tag2": "value2"}

    assert members[2]["votes"] == 1
    assert members[2]["priority"] == 0.5
    assert members[2]["tags"] == {"environment": "prod", "tag2": "value2"}
