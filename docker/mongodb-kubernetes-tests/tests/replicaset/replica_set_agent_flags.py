from typing import Optional

from kubernetes import client
from kubetester import create_or_update_configmap, find_fixture, random_k8s_name, read_configmap, try_load, wait_until
from kubetester.kubetester import KubernetesTester, ensure_ent_version
from kubetester.mongodb import MongoDB
from kubetester.phase import Phase
from pytest import fixture, mark
from tests.pod_logs import assert_log_types_in_structured_json_pod_log, get_all_default_log_types, get_all_log_types

custom_agent_log_path = "/var/log/mongodb-mms-automation/customLogFile"
custom_readiness_log_path = "/var/log/mongodb-mms-automation/customReadinessLogFile"
custom_monitoring_log_path = "/var/log/mongodb-mms-automation/custom-monitoring-agent.log"
custom_backup_log_path = "/var/log/mongodb-mms-automation/custom-backup-agent.log"
outside_mount_monitoring_log_path = "/agent-logs/monitoring-agent.log"
outside_mount_backup_log_path = "/agent-logs/backup-agent.log"


@fixture(scope="module")
def project_name_prefix(namespace: str) -> str:
    return random_k8s_name(f"{namespace}-project")


@fixture(scope="module")
def first_project(namespace: str, project_name_prefix: str) -> str:
    cm = read_configmap(namespace=namespace, name="my-project")
    project_name = f"{project_name_prefix}-first"
    return create_or_update_configmap(
        namespace=namespace,
        name=project_name,
        data={
            "baseUrl": cm["baseUrl"],
            "projectName": project_name,
            "orgId": cm["orgId"],
        },
    )


@fixture(scope="module")
def second_project(namespace: str, project_name_prefix: str) -> str:
    cm = read_configmap(namespace=namespace, name="my-project")
    project_name = project_name_prefix
    return create_or_update_configmap(
        namespace=namespace,
        name=project_name,
        data={
            "baseUrl": cm["baseUrl"],
            "projectName": project_name,
            "orgId": cm["orgId"],
        },
    )


@fixture(scope="module")
def replica_set(namespace: str, first_project: str, custom_mdb_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(find_fixture("replica-set-basic.yaml"), namespace=namespace, name="replica-set")
    resource.set_version(ensure_ent_version(custom_mdb_version))
    resource.set_architecture_annotation()
    resource["spec"]["opsManager"]["configMapRef"]["name"] = first_project

    try_load(resource)
    return resource


@fixture(scope="module")
def second_replica_set(namespace: str, second_project: str, custom_mdb_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(find_fixture("replica-set-basic.yaml"), namespace=namespace, name="replica-set-2")
    resource.set_version(ensure_ent_version(custom_mdb_version))
    resource["spec"]["opsManager"]["configMapRef"]["name"] = second_project

    try_load(resource)
    return resource


@mark.e2e_replica_set_agent_flags_and_readinessProbe
def test_replica_set(replica_set: MongoDB):
    replica_set.update()
    replica_set.assert_reaches_phase(Phase.Running, timeout=400)


@mark.e2e_replica_set_agent_flags_and_readinessProbe
def test_second_replica_set(second_replica_set: MongoDB):
    second_replica_set.update()
    second_replica_set.assert_reaches_phase(Phase.Running, timeout=400)


@mark.e2e_replica_set_agent_flags_and_readinessProbe
def test_log_types_with_default_automation_log_file(replica_set: MongoDB):
    assert_pod_log_types(replica_set, get_all_default_log_types())


@mark.e2e_replica_set_agent_flags_and_readinessProbe
def test_set_custom_log_file(replica_set: MongoDB):
    replica_set.load()
    replica_set["spec"]["agent"] = {
        "startupOptions": {
            "logFile": custom_agent_log_path,
            "maxLogFileSize": "10485760",
            "maxLogFiles": "5",
            "maxLogFileDurationHrs": "16",
            "logFile": "/var/log/mongodb-mms-automation/customLogFile",
        }
    }
    replica_set["spec"]["agent"].setdefault("readinessProbe", {})
    # LOG_FILE_PATH is an env var used by the readinessProbe to configure where we log to
    replica_set["spec"]["agent"]["readinessProbe"] = {
        "environmentVariables": {"LOG_FILE_PATH": custom_readiness_log_path}
    }
    replica_set.update()

    replica_set.assert_reaches_phase(Phase.Running, timeout=400)


@mark.e2e_replica_set_agent_flags_and_readinessProbe
def test_replica_set_has_agent_flags(replica_set: MongoDB, namespace: str):
    cmd = [
        "/bin/sh",
        "-c",
        "ls /var/log/mongodb-mms-automation/customLogFile* | wc -l",
    ]
    for i in range(3):
        result = KubernetesTester.run_command_in_pod_container(
            f"replica-set-{i}",
            namespace,
            cmd,
        )
        assert result != "0"


@mark.e2e_replica_set_agent_flags_and_readinessProbe
def test_log_readiness_probe_path_set_via_env_var(replica_set: MongoDB, namespace: str):
    cmd = [
        "/bin/sh",
        "-c",
        f"ls {custom_readiness_log_path}* | wc -l",
    ]
    for i in range(3):
        result = KubernetesTester.run_command_in_pod_container(
            f"replica-set-{i}",
            namespace,
            cmd,
        )
        assert result != "0"


@mark.e2e_replica_set_agent_flags_and_readinessProbe
def test_log_types_with_custom_automation_log_file(replica_set: MongoDB):
    assert_pod_log_types(replica_set, get_all_default_log_types())


@mark.e2e_replica_set_agent_flags_and_readinessProbe
def test_enable_audit_log(replica_set: MongoDB):
    additional_mongod_config = {
        "auditLog": {
            "destination": "file",
            "format": "JSON",
            "path": "/var/log/mongodb-mms-automation/mongodb-audit-changed.log",
        }
    }
    replica_set["spec"]["additionalMongodConfig"] = additional_mongod_config
    replica_set.update()

    replica_set.assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_replica_set_agent_flags_and_readinessProbe
def test_log_types_with_audit_enabled(replica_set: MongoDB):
    assert_pod_log_types(replica_set, get_all_log_types())


@mark.e2e_replica_set_agent_flags_and_readinessProbe
def test_set_custom_monitoring_and_backup_log_paths(replica_set: MongoDB):
    replica_set.load()
    replica_set["spec"]["agent"]["monitoringAgent"] = {
        "logFilePath": custom_monitoring_log_path,
    }
    replica_set["spec"]["agent"]["backupAgent"] = {
        "logFilePath": custom_backup_log_path,
    }
    replica_set.update()
    replica_set.assert_reaches_phase(Phase.Running, timeout=400)


@mark.e2e_replica_set_agent_flags_and_readinessProbe
def test_custom_monitoring_log_path_env_var(replica_set: MongoDB, namespace: str):
    for i in range(3):
        pod = client.CoreV1Api().read_namespaced_pod(f"replica-set-{i}", namespace)
        env_vars = {var.name: var.value for var in pod.spec.containers[0].env if var.value is not None}
        assert env_vars.get("MDB_LOG_FILE_MONITORING_AGENT") == custom_monitoring_log_path, (
            f"Expected MDB_LOG_FILE_MONITORING_AGENT={custom_monitoring_log_path}, "
            f"got {env_vars.get('MDB_LOG_FILE_MONITORING_AGENT')}"
        )


@mark.e2e_replica_set_agent_flags_and_readinessProbe
def test_custom_backup_log_path_env_var(replica_set: MongoDB, namespace: str):
    for i in range(3):
        pod = client.CoreV1Api().read_namespaced_pod(f"replica-set-{i}", namespace)
        env_vars = {var.name: var.value for var in pod.spec.containers[0].env if var.value is not None}
        assert env_vars.get("MDB_LOG_FILE_BACKUP_AGENT") == custom_backup_log_path, (
            f"Expected MDB_LOG_FILE_BACKUP_AGENT={custom_backup_log_path}, "
            f"got {env_vars.get('MDB_LOG_FILE_BACKUP_AGENT')}"
        )


@mark.e2e_replica_set_agent_flags_and_readinessProbe
def test_set_log_paths_outside_standard_mount(replica_set: MongoDB, namespace: str):
    """Log paths outside /var/log/mongodb-mms-automation should get their own emptyDir mount."""
    replica_set.load()
    replica_set["spec"].setdefault("agent", {})
    replica_set["spec"]["agent"]["monitoringAgent"] = {"logFilePath": outside_mount_monitoring_log_path}
    replica_set["spec"]["agent"]["backupAgent"] = {"logFilePath": outside_mount_backup_log_path}
    replica_set.update()
    replica_set.assert_reaches_phase(Phase.Running, timeout=400)

    expected_paths = (outside_mount_monitoring_log_path, outside_mount_backup_log_path)
    for i in range(3):
        pod = f"replica-set-{i}"
        for log_path in expected_paths:
            cmd = ["/bin/sh", "-c", f"test -f {log_path} && echo present || echo missing"]

            def file_present(p=pod, c=cmd) -> bool:
                return "present" in KubernetesTester.run_command_in_pod_container(p, namespace, c)

            wait_until(
                file_present,
                timeout=400,
                sleep_time=5,
                msg=f"log file {log_path} to appear on {pod}",
            )


@mark.e2e_replica_set_agent_flags_and_readinessProbe
def test_default_monitoring_and_backup_log_paths(second_replica_set: MongoDB, namespace: str):
    """Verify that when no custom log paths are set, the default paths are used."""
    default_monitoring_path = "/var/log/mongodb-mms-automation/monitoring-agent.log"
    default_backup_path = "/var/log/mongodb-mms-automation/backup-agent.log"
    for i in range(3):
        pod = client.CoreV1Api().read_namespaced_pod(f"replica-set-2-{i}", namespace)
        env_vars = {var.name: var.value for var in pod.spec.containers[0].env if var.value is not None}
        assert env_vars.get("MDB_LOG_FILE_MONITORING_AGENT") == default_monitoring_path, (
            f"Expected default MDB_LOG_FILE_MONITORING_AGENT={default_monitoring_path}, "
            f"got {env_vars.get('MDB_LOG_FILE_MONITORING_AGENT')}"
        )
        assert env_vars.get("MDB_LOG_FILE_BACKUP_AGENT") == default_backup_path, (
            f"Expected default MDB_LOG_FILE_BACKUP_AGENT={default_backup_path}, "
            f"got {env_vars.get('MDB_LOG_FILE_BACKUP_AGENT')}"
        )


def assert_pod_log_types(replica_set: MongoDB, expected_log_types: Optional[set[str]]):
    for i in range(3):
        assert_log_types_in_structured_json_pod_log(
            replica_set.namespace, f"{replica_set.name}-{i}", expected_log_types
        )
