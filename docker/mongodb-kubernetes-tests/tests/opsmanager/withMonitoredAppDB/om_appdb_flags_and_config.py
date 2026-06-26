from typing import Optional

from kubetester import find_fixture
from kubetester.kubetester import KubernetesTester
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.phase import Phase
from pytest import fixture, mark
from tests.conftest import is_multi_cluster
from tests.opsmanager.withMonitoredAppDB.conftest import enable_multi_cluster_deployment


@fixture(scope="module")
def ops_manager(namespace: str, custom_version: Optional[str], custom_appdb_version: str) -> MongoDBOpsManager:
    resource = MongoDBOpsManager.from_yaml(
        find_fixture("om_validation.yaml"), namespace=namespace, name="om-agent-flags"
    )

    # both monitoring and automation agent should see these changes
    resource["spec"]["applicationDatabase"]["agent"] = {
        "startupOptions": {"logFile": "/var/log/mongodb-mms-automation/customLogFile"}
    }
    member1_config = {
        "votes": 1,
        "priority": "0.5",
        "tags": {
            "tag1": "value1",
            "environment": "prod",
        },
    }
    member2_config = {
        "votes": 1,
        "priority": "1.5",
        "tags": {
            "tag2": "value2",
            "environment": "prod",
        },
    }
    member3_config = {
        "votes": 1,
        "priority": "0.5",
        "tags": {
            "tag2": "value2",
            "environment": "prod",
        },
    }

    resource.set_version(custom_version)
    resource.set_appdb_version(custom_appdb_version)

    if is_multi_cluster():
        enable_multi_cluster_deployment(
            resource,
            appdb_cluster_spec_list=[1, 2],
            appdb_member_configs=[[member1_config], [member2_config, member3_config]],
        )
    else:
        resource["spec"]["applicationDatabase"]["memberConfig"] = [member1_config, member2_config, member3_config]

    resource.update()
    return resource


@mark.e2e_om_appdb_flags_and_config
def test_appdb(ops_manager: MongoDBOpsManager):
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=900)


@mark.e2e_om_appdb_flags_and_config
def test_om(ops_manager: MongoDBOpsManager):
    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=800)


@mark.e2e_om_appdb_flags_and_config
def test_monitoring_is_configured(ops_manager: MongoDBOpsManager):
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_om_appdb_flags_and_config
def test_appdb_has_agent_flags(ops_manager: MongoDBOpsManager):
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
            container="mongodb-agent",
            api_client=api_client,
        )
        assert result != "0"


@mark.e2e_om_appdb_flags_and_config
def test_monitoring_credentials_not_in_automation_config(ops_manager: MongoDBOpsManager):
    """Under Option B the monitoring credentials are delivered to the single agent as
    -mmsGroupId/-mmsApiKey CLI flags, so they must NOT appear in the automation config's
    monitoringVersions.additionalParams (keeping the API key out of the AC secret).
    """
    ac_tester = ops_manager.get_automation_config_tester()
    monitoring_versions = ac_tester.automation_config.get("monitoringVersions", [])
    assert len(monitoring_versions) > 0, "monitoringVersions should be configured in the automation config"
    for mv in monitoring_versions:
        params = mv.get("additionalParams", {})
        assert "mmsGroupId" not in params, "mmsGroupId must not be embedded in monitoring additionalParams"
        assert "mmsApiKey" not in params, "mmsApiKey must not be embedded in monitoring additionalParams"


@mark.e2e_om_appdb_flags_and_config
def test_monitoring_reaches_om(ops_manager: MongoDBOpsManager):
    """End-to-end check that the single agent (which receives the monitoring credentials as
    -mmsGroupId/-mmsApiKey CLI flags, not via AC additionalParams) actually reports monitoring
    data to OM: here we query OM for the hosts in the AppDB project and assert every AppDB
    process hostname is present AND actively monitored (non-empty lastPing), then confirm real
    monitoring measurements have arrived.
    """
    expected_hostnames = ops_manager.get_appdb_hostnames_for_monitoring()
    assert len(expected_hostnames) > 0, "expected at least one AppDB hostname for monitoring"

    def appdb_hosts_are_actively_monitored() -> bool:
        hosts_by_name = {h["hostname"]: h for h in ops_manager.get_appdb_hosts()}
        for hostname in expected_hostnames:
            host = hosts_by_name.get(hostname)
            # lastPing is set by OM once a monitoring agent reports for this host.
            if host is None or not host.get("lastPing"):
                return False
        return True

    KubernetesTester.wait_until(appdb_hosts_are_actively_monitored, timeout=600, sleep_time=10)
    ops_manager.assert_monitoring_data_exists(timeout=600)


@mark.e2e_om_appdb_flags_and_config
def test_appdb_flags_changed(ops_manager: MongoDBOpsManager):
    ops_manager.load()
    ops_manager["spec"]["applicationDatabase"]["agent"]["startupOptions"]["dialTimeoutSeconds"] = "70"
    ops_manager.update()
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_om_appdb_flags_and_config
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


@mark.e2e_om_appdb_flags_and_config
def test_automation_config_secret_member_options(ops_manager: MongoDBOpsManager):
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


@mark.e2e_om_appdb_flags_and_config
def test_update_appdb_member_options(ops_manager: MongoDBOpsManager):
    member1_config = {
        "votes": 1,
        "priority": "0.5",
        "tags": {
            "tag1": "value1",
            "environment": "prod",
        },
    }
    member2_config = {
        "votes": 1,
        "priority": "1.5",
        "tags": {
            "tag2": "value2",
            "environment": "prod",
        },
    }
    member3_config = {
        "votes": 0,
        "priority": "0",
        "tags": {
            "tag2": "value2",
            "environment": "prod",
        },
    }
    ops_manager.load()
    if is_multi_cluster():
        enable_multi_cluster_deployment(
            ops_manager,
            appdb_cluster_spec_list=[1, 2],
            appdb_member_configs=[[member1_config], [member2_config, member3_config]],
        )
    else:
        ops_manager["spec"]["applicationDatabase"]["memberConfig"] = [member1_config, member2_config, member3_config]
    ops_manager.update()

    ops_manager.appdb_status().assert_abandons_phase(Phase.Running)
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=900)


@mark.e2e_om_appdb_flags_and_config
def test_automation_config_secret_updated_member_options(ops_manager: MongoDBOpsManager):
    members = ops_manager.get_automation_config_tester().get_replica_set_members(ops_manager.app_db_name())

    assert members[0]["votes"] == 1
    assert members[0]["priority"] == 0.5
    assert members[0]["tags"] == {"environment": "prod", "tag1": "value1"}

    assert members[1]["votes"] == 1
    assert members[1]["priority"] == 1.5
    assert members[1]["tags"] == {"environment": "prod", "tag2": "value2"}

    assert members[2]["votes"] == 0
    assert members[2]["priority"] == 0.0
    assert members[2]["tags"] == {"environment": "prod", "tag2": "value2"}


@mark.e2e_om_appdb_flags_and_config
def test_scale_up_appdb_with_member_options(ops_manager: MongoDBOpsManager):
    member1_config = {
        "votes": 1,
        "priority": "0.5",
        "tags": {
            "tag1": "value1",
            "environment": "prod",
        },
    }
    member2_config = {
        "votes": 1,
        "priority": "1.5",
        "tags": {
            "tag2": "value2",
            "environment": "prod",
        },
    }
    member3_config = {
        "votes": 0,
        "priority": "0",
        "tags": {
            "tag2": "value2",
            "environment": "prod",
        },
    }
    member4_config = {
        "votes": 1,
        "priority": "2.0",
        "tags": {
            "tag2": "value4",
            "environment": "superprod",
        },
    }
    member5_config = {
        "votes": 1,
        "priority": "3.0",
        "tags": {
            "tag2": "value5",
            "environment": "superprod",
        },
    }

    ops_manager.load()
    if is_multi_cluster():
        enable_multi_cluster_deployment(
            ops_manager,
            appdb_cluster_spec_list=[2, 3],
            appdb_member_configs=[[member1_config, member4_config], [member2_config, member3_config, member5_config]],
        )
    else:
        ops_manager["spec"]["applicationDatabase"]["memberConfig"] = [
            member1_config,
            member2_config,
            member3_config,
            member4_config,
            member5_config,
        ]
        ops_manager["spec"]["applicationDatabase"]["members"] = 5
    ops_manager.update()

    ops_manager.appdb_status().assert_abandons_phase(Phase.Running)
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=900)
    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=900)


@mark.e2e_om_appdb_flags_and_config
def test_automation_config_secret_scale_up_updated_member_options(ops_manager: MongoDBOpsManager):
    members = ops_manager.get_automation_config_tester().get_replica_set_members(ops_manager.app_db_name())

    assert members[0]["votes"] == 1
    assert members[0]["priority"] == 0.5
    assert members[0]["tags"] == {"environment": "prod", "tag1": "value1"}

    assert members[1]["votes"] == 1
    assert members[1]["priority"] == 1.5
    assert members[1]["tags"] == {"environment": "prod", "tag2": "value2"}

    assert members[2]["votes"] == 0
    assert members[2]["priority"] == 0.0
    assert members[2]["tags"] == {"environment": "prod", "tag2": "value2"}

    assert members[3]["votes"] == 1
    assert members[3]["priority"] == 2.0
    assert members[3]["tags"] == {"environment": "superprod", "tag2": "value4"}

    assert members[4]["votes"] == 1
    assert members[4]["priority"] == 3.0
    assert members[4]["tags"] == {"environment": "superprod", "tag2": "value5"}


@mark.e2e_om_appdb_flags_and_config
def test_scale_down_appdb__with_member_options(ops_manager: MongoDBOpsManager):
    member1_config = {
        "votes": 1,
        "priority": "0.5",
        "tags": {
            "tag1": "value1",
            "environment": "prod",
        },
    }
    member2_config = {
        "votes": 1,
        "priority": "1.5",
        "tags": {
            "tag2": "value2",
            "environment": "prod",
        },
    }
    member4_config = {
        "votes": 1,
        "priority": "1.7",
        "tags": {
            "tag2": "value4",
            "environment": "superprod",
        },
    }

    ops_manager.load()
    if is_multi_cluster():
        enable_multi_cluster_deployment(
            ops_manager,
            appdb_cluster_spec_list=[2, 1],
            appdb_member_configs=[[member1_config, member4_config], [member2_config]],
        )
    else:
        ops_manager["spec"]["applicationDatabase"]["memberConfig"] = [
            member1_config,
            member2_config,
            member4_config,
        ]
        ops_manager["spec"]["applicationDatabase"]["members"] = 3
    ops_manager.update()

    ops_manager.appdb_status().assert_abandons_phase(Phase.Running)
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=900)
    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=900)


@mark.e2e_om_appdb_flags_and_config
def test_automation_config_secret_scale_down_updated_member_options(ops_manager: MongoDBOpsManager):
    members = ops_manager.get_automation_config_tester().get_replica_set_members(ops_manager.app_db_name())

    assert members[0]["votes"] == 1
    assert members[0]["priority"] == 0.5
    assert members[0]["tags"] == {"environment": "prod", "tag1": "value1"}

    assert members[1]["votes"] == 1
    assert members[1]["priority"] == 1.5
    assert members[1]["tags"] == {"environment": "prod", "tag2": "value2"}

    assert members[2]["votes"] == 1
    assert members[2]["priority"] == 1.7
    assert members[2]["tags"] == {"environment": "superprod", "tag2": "value4"}
