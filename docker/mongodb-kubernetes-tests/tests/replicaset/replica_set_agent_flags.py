from typing import Optional

from kubetester import create_or_update_configmap, find_fixture, random_k8s_name, read_configmap, try_load
from kubetester.kubetester import KubernetesTester, ensure_ent_version, is_default_architecture_static
from kubetester.mongodb import MongoDB
from kubetester.phase import Phase
from pytest import fixture, mark
from tests.pod_logs import assert_log_types_in_structured_json_pod_log, get_all_default_log_types, get_all_log_types

custom_agent_log_path = "/var/log/mongodb-mms-automation/customLogFile"
custom_readiness_log_path = "/var/log/mongodb-mms-automation/customReadinessLogFile"
default_download_base = "/var/lib/mongodb-mms-automation"
custom_download_base = "/var/lib/mongodb-mms-automation-custom"
brand_new_download_base = "/custom/custom-download-base"


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
def replica_set(namespace: str, first_project: str, custom_mdb_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(find_fixture("replica-set-basic.yaml"), namespace=namespace, name="replica-set")
    resource.set_version(ensure_ent_version(custom_mdb_version))
    resource.set_architecture_annotation()
    resource["spec"]["opsManager"]["configMapRef"]["name"] = first_project

    try_load(resource)
    return resource


@mark.e2e_replica_set_agent_flags_and_readinessProbe
def test_replica_set(replica_set: MongoDB):
    replica_set.update()
    replica_set.assert_reaches_phase(Phase.Running, timeout=400 if is_default_architecture_static() else 700)


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

    replica_set.assert_reaches_phase(Phase.Running, timeout=400 if is_default_architecture_static() else 900)


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

    replica_set.assert_reaches_phase(Phase.Running, timeout=600 if is_default_architecture_static() else 900)


@mark.e2e_replica_set_agent_flags_and_readinessProbe
def test_log_types_with_audit_enabled(replica_set: MongoDB):
    assert_pod_log_types(replica_set, get_all_log_types())


@mark.e2e_replica_set_agent_flags_and_readinessProbe
def test_default_download_base_in_automation_config(replica_set: MongoDB):
    options = replica_set.get_automation_config_tester().automation_config.get("options", {})
    assert options.get("downloadBase") == default_download_base


@mark.e2e_replica_set_agent_flags_and_readinessProbe
def test_set_custom_download_base(replica_set: MongoDB):
    replica_set.load()
    replica_set["spec"]["downloadBase"] = custom_download_base
    replica_set.update()
    # Non-static: the operator mounts the same agent emptyDir subPath at both the custom path and
    # the historical default, so stale OM automation config still sees consistent files during rollout.
    # Allow extra time for multi-pod rollout and agent/version convergence when tightening timeouts.
    replica_set.assert_reaches_phase(Phase.Running, timeout=600 if is_default_architecture_static() else 2700)


@mark.e2e_replica_set_agent_flags_and_readinessProbe
def test_custom_download_base_in_automation_config(replica_set: MongoDB):
    options = replica_set.get_automation_config_tester().automation_config.get("options", {})
    assert options.get("downloadBase") == custom_download_base


@mark.e2e_replica_set_agent_flags_and_readinessProbe
def test_set_brand_new_download_base(replica_set: MongoDB):
    replica_set.load()
    replica_set["spec"]["downloadBase"] = brand_new_download_base
    replica_set.update()
    replica_set.assert_reaches_phase(Phase.Running, timeout=600 if is_default_architecture_static() else 2700)


@mark.e2e_replica_set_agent_flags_and_readinessProbe
def test_brand_new_download_base_in_automation_config(replica_set: MongoDB):
    options = replica_set.get_automation_config_tester().automation_config.get("options", {})
    assert options.get("downloadBase") == brand_new_download_base


@mark.e2e_replica_set_agent_flags_and_readinessProbe
def test_enable_scram_auth(replica_set: MongoDB):
    replica_set.load()
    replica_set["spec"]["security"] = replica_set["spec"].get("security", {})
    replica_set["spec"]["security"]["authentication"] = {
        "enabled": True,
        "modes": ["SCRAM"],
    }
    replica_set.update()
    replica_set.assert_reaches_phase(Phase.Running, timeout=600 if is_default_architecture_static() else 900)


@mark.e2e_replica_set_agent_flags_and_readinessProbe
def test_keyfile_follows_download_base_by_default(replica_set: MongoDB):
    replica_set.load()
    expected_keyfile = f"{replica_set['spec']['downloadBase']}/keyfile"
    auth = replica_set.get_automation_config_tester().automation_config.get("auth", {})
    assert auth.get("keyfile") == expected_keyfile


def assert_pod_log_types(replica_set: MongoDB, expected_log_types: Optional[set[str]]):
    for i in range(3):
        assert_log_types_in_structured_json_pod_log(
            replica_set.namespace, f"{replica_set.name}-{i}", expected_log_types
        )
