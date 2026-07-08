"""Test that agent and MongoDB logs are sent to the pod's stdout (kubectl logs),
not to log files inside the container."""

import json

from kubetester.kubetester import KubernetesTester, ensure_ent_version
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.kubetester import is_default_architecture_static
from kubetester.mongodb import MongoDB
from kubetester.phase import Phase
from pytest import fixture, mark

RESOURCE_NAME = "rs-stdout-test"


def _container_name() -> str:
    return "mongodb-agent" if is_default_architecture_static() else "mongodb-enterprise-database"


@fixture(scope="module")
def replica_set(namespace: str, custom_mdb_version: str, cluster_domain: str) -> MongoDB:
    resource = MongoDB.from_yaml(yaml_fixture("replica-set.yaml"), RESOURCE_NAME, namespace)
    resource.set_version(ensure_ent_version(custom_mdb_version))
    resource["spec"]["clusterDomain"] = cluster_domain
    resource["spec"]["members"] = 1
    return resource


@fixture(scope="module")
def deployed_replica_set(replica_set: MongoDB) -> MongoDB:
    replica_set.update()
    replica_set.assert_reaches_phase(Phase.Running, timeout=600)
    return replica_set


@mark.e2e_replica_set_stdout_logging
def test_agent_logs_in_stdout(deployed_replica_set: MongoDB):
    """Pod stdout must contain automation-agent log lines."""
    namespace = deployed_replica_set.namespace
    pod_name = f"{deployed_replica_set.name}-0"

    logs = KubernetesTester.read_pod_logs(namespace, pod_name, _container_name())

    has_agent_launcher_logs = "[agent-launcher]" in logs
    has_agent_debug_logs = "[.debug]" in logs or "[.info]" in logs

    assert has_agent_launcher_logs and has_agent_debug_logs, (
        f"Expected both [agent-launcher] and [.debug]/[.info] markers in stdout for "
        f"{namespace}/{pod_name}. launcher={has_agent_launcher_logs} "
        f"debug/info={has_agent_debug_logs} log_length={len(logs)}"
    )


@mark.e2e_replica_set_stdout_logging
def test_mongodb_json_logs_in_stdout(deployed_replica_set: MongoDB):
    """Pod stdout must contain at least one real mongod JSON log line.

    A genuine mongod log line is a JSON object with the fields t (timestamp), s (severity),
    c (component), ctx (context) and msg. We must not accept incidental matches from
    agent debug output that happens to mention these strings.
    """
    namespace = deployed_replica_set.namespace
    pod_name = f"{deployed_replica_set.name}-0"

    logs = KubernetesTester.read_pod_logs(namespace, pod_name, _container_name())

    mongod_lines = []
    for line in logs.splitlines():
        line = line.strip()
        if not line.startswith("{"):
            continue
        try:
            obj = json.loads(line)
        except json.JSONDecodeError:
            continue
        inner = json.loads(obj["msg"]) if isinstance(obj.get("msg"), str) else obj
        if all(k in inner for k in ("t", "s", "c", "ctx", "msg")):
            mongod_lines.append(inner)

    assert len(mongod_lines) > 0, (
        f"Expected at least one mongod JSON log line in stdout for {namespace}/{pod_name}, "
        f"got 0. Total log length: {len(logs)}"
    )


@mark.e2e_replica_set_stdout_logging
def test_only_module_log_tails(deployed_replica_set: MongoDB):
    """Main automation-agent and mongod no longer use tails. Only the audit-log file
    is tailed to stdout; monitoring and backup log to stderr directly — at most 1 tail process."""
    namespace = deployed_replica_set.namespace
    pod_name = f"{deployed_replica_set.name}-0"

    result = KubernetesTester.run_command_in_pod_container(
        pod_name, namespace, ["sh", "-c", "pgrep tail | wc -l"], container=_container_name()
    )
    tail_count = int(result.strip())

    assert tail_count <= 1, (
        f"Expected at most 1 tail process (audit-log) in " f"{namespace}/{pod_name}, got {tail_count}"
    )


@mark.e2e_replica_set_stdout_logging
def test_monitoring_agent_logs_in_stdout(deployed_replica_set: MongoDB):
    """Monitoring agent module logs must reach pod stdout."""
    namespace = deployed_replica_set.namespace
    pod_name = f"{deployed_replica_set.name}-0"

    logs = KubernetesTester.read_pod_logs(namespace, pod_name, _container_name())

    assert "<Monitoring Module Manager>" in logs, (
        f"Expected '<Monitoring Module Manager>' tagged lines in stdout for "
        f"{namespace}/{pod_name}, got none. log_length={len(logs)}"
    )


@mark.e2e_replica_set_stdout_logging
def test_backup_agent_logs_in_stdout(deployed_replica_set: MongoDB):
    """Backup agent module logs must reach pod stdout."""
    namespace = deployed_replica_set.namespace
    pod_name = f"{deployed_replica_set.name}-0"

    logs = KubernetesTester.read_pod_logs(namespace, pod_name, _container_name())

    assert "<Backup Module Manager>" in logs, (
        f"Expected '<Backup Module Manager>' tagged lines in stdout for "
        f"{namespace}/{pod_name}, got none. log_length={len(logs)}"
    )


@mark.e2e_replica_set_stdout_logging
def test_no_log_files_written(deployed_replica_set: MongoDB):
    """The historical agent/mongod log files must not exist — every component logs to stdout."""
    namespace = deployed_replica_set.namespace
    pod_name = f"{deployed_replica_set.name}-0"

    expected_absent = [
        "/var/log/mongodb-mms-automation/mongodb.log",
        "/data/mongodb.log",
        "/var/log/mongodb-mms-automation/automation-agent.log",
        "/var/log/mongodb-mms-automation/automation-agent-verbose.log",
        "/var/log/mongodb-mms-automation/automation-agent-stderr.log",
    ]
    result = KubernetesTester.run_command_in_pod_container(
        pod_name,
        namespace,
        ["sh", "-c", "ls " + " ".join(expected_absent) + " 2>/dev/null | wc -l"],
        container=_container_name(),
    )
    file_count = result.strip()

    assert file_count == "0", (
        f"Expected none of {expected_absent} to exist in {namespace}/{pod_name}, " f"got {file_count} present"
    )


@mark.e2e_replica_set_stdout_logging
def test_audit_logs_in_stdout(deployed_replica_set: MongoDB):
    """When auditing is enabled to file, the launcher's tail must surface audit lines on stdout."""
    deployed_replica_set["spec"]["additionalMongodConfig"] = {
        "auditLog": {
            "destination": "file",
            "format": "JSON",
            "path": "/var/log/mongodb-mms-automation/mongodb-audit.log",
        }
    }
    deployed_replica_set.update()
    deployed_replica_set.assert_reaches_phase(Phase.Running, timeout=600)

    namespace = deployed_replica_set.namespace
    pod_name = f"{deployed_replica_set.name}-0"

    logs = KubernetesTester.read_pod_logs(namespace, pod_name, _container_name())

    audit_lines = []
    for line in logs.splitlines():
        line = line.strip()
        if not line.startswith("{"):
            continue
        try:
            obj = json.loads(line)
        except json.JSONDecodeError:
            continue
        inner = json.loads(obj["msg"]) if isinstance(obj.get("msg"), str) else obj
        if "atype" in inner and "ts" in inner:
            audit_lines.append(inner)

    assert len(audit_lines) > 0, (
        f"Expected at least one mongod audit JSON line (atype/ts) in stdout for "
        f"{namespace}/{pod_name}. log_length={len(logs)}"
    )
