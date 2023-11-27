from typing import Optional

from pytest import mark, fixture

from kubetester import find_fixture, create_or_update

from kubetester.mongodb import MongoDB, Phase

from kubetester.kubetester import KubernetesTester
from tests.opsmanager.conftest import ensure_ent_version
from tests.pod_logs import (
    get_all_default_log_types,
    assert_log_types_in_structured_json_pod_log,
    get_all_log_types,
)


@fixture(scope="module")
def replica_set(namespace: str, custom_mdb_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        find_fixture("replica-set-basic.yaml"), namespace=namespace
    )

    resource.set_version(ensure_ent_version(custom_mdb_version))

    create_or_update(resource)
    return resource


@mark.e2e_replica_set_agent_flags
def test_replica_set(replica_set: MongoDB):
    replica_set.assert_reaches_phase(Phase.Running, timeout=400)


@mark.e2e_replica_set_agent_flags
def test_log_types_with_default_automation_log_file(replica_set: MongoDB):
    assert_pod_log_types(replica_set, get_all_default_log_types())


@mark.e2e_replica_set_agent_flags
def test_set_custom_log_file(replica_set: MongoDB):
    replica_set.load()
    replica_set["spec"]["agent"] = {
        "startupOptions": {"logFile": "/var/log/mongodb-mms-automation/customLogFile"}
    }
    create_or_update(replica_set)

    replica_set.assert_reaches_phase(Phase.Running, timeout=400)


@mark.e2e_replica_set_agent_flags
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


@mark.e2e_replica_set_agent_flags
def test_log_types_with_custom_automation_log_file(replica_set: MongoDB):
    assert_pod_log_types(replica_set, get_all_default_log_types())


@mark.e2e_replica_set_agent_flags
def test_enable_audit_log(replica_set: MongoDB):
    additional_mongod_config = {
        "auditLog": {
            "destination": "file",
            "format": "JSON",
            "path": "/var/log/mongodb-mms-automation/mongodb-audit-changed.log",
        }
    }
    replica_set["spec"]["additionalMongodConfig"] = additional_mongod_config
    create_or_update(replica_set)

    replica_set.assert_reaches_phase(Phase.Running, timeout=400)


@mark.e2e_replica_set_agent_flags
def test_log_types_with_audit_enabled(replica_set: MongoDB):
    assert_pod_log_types(replica_set, get_all_log_types())


def assert_pod_log_types(replica_set: MongoDB, expected_log_types: Optional[set[str]]):
    for i in range(3):
        assert_log_types_in_structured_json_pod_log(
            replica_set.namespace, f"{replica_set.name}-{i}", expected_log_types
        )
