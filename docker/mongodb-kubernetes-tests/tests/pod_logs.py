import json
import logging
from typing import Any, Optional

import kubernetes
from kubetester.kubetester import KubernetesTester, is_default_architecture_static


def parse_json_pod_logs(pod_logs: str) -> list[dict[str, Any]]:
    """Parses pod logs returned as a string and returns list of lines parsed from structured json."""
    lines = pod_logs.strip().split("\n")
    log_lines = []
    for line in lines:
        try:
            log_lines.append(json.loads(line))
        except json.JSONDecodeError as e:
            logging.warning(f"Ignoring the following log line {line} because of {e}")
    return log_lines


def get_agent_logs(logs: list[dict[str, Any]]) -> list[dict[str, Any]]:
    """Filter for automation agent logs by process tag."""
    return [log for log in logs if log.get("process") == "agent"]


def get_mongodb_logs(logs: list[dict[str, Any]]) -> list[dict[str, Any]]:
    """Filter for mongod logs by process tag."""
    return [log for log in logs if log.get("process") == "mongod"]


def get_audit_logs(logs: list[dict[str, Any]]) -> list[dict[str, Any]]:
    """Filter for audit logs by process tag, unwrapping the inner JSON msg payload."""
    audit = []
    for log in logs:
        if log.get("process") != "audit":
            continue
        msg = log.get("msg")
        if isinstance(msg, str):
            try:
                inner = json.loads(msg)
            except json.JSONDecodeError:
                continue
            if isinstance(inner, dict):
                audit.append(inner)
        elif isinstance(msg, dict):
            audit.append(msg)
    return audit


def get_pod_logs(
    namespace: str,
    pod_name: str,
    container_name: str,
    api_client: Optional[kubernetes.client.ApiClient] = None,
) -> list[dict[str, Any]]:
    """Read logs from pod_name and return parsed JSON log lines."""
    pod_logs_str = KubernetesTester.read_pod_logs(namespace, pod_name, container_name, api_client=api_client)
    return parse_json_pod_logs(pod_logs_str)


def assert_logs_present_in_stdout(
    namespace: str,
    pod_name: str,
    api_client: Optional[kubernetes.client.ApiClient] = None,
    container_name: str = "mongodb-agent",
):
    """
    Checks that agent and MongoDB logs appear in pod stdout.
    """

    if not is_default_architecture_static():
        container_name = "mongodb-enterprise-database"

    logs = get_pod_logs(namespace, pod_name, container_name, api_client=api_client)

    agent_logs = get_agent_logs(logs)
    mongodb_logs = get_mongodb_logs(logs)

    assert len(agent_logs) > 0, f"pod {namespace}/{pod_name} should have agent logs in stdout"
    assert len(mongodb_logs) > 0, f"pod {namespace}/{pod_name} should have mongodb logs in stdout"


# Legacy functions for backward compatibility with existing tests
# These support the old {"logType": ..., "contents": ...} wrapper format


def get_structured_json_pod_logs(
    namespace: str,
    pod_name: str,
    container_name: str,
    api_client: Optional[kubernetes.client.ApiClient] = None,
) -> dict[str, list[str]]:
    """Read logs from pod_name and groups the lines by logType (legacy format)."""
    pod_logs_str = KubernetesTester.read_pod_logs(namespace, pod_name, container_name, api_client=api_client)
    log_lines = parse_json_pod_logs(pod_logs_str)

    log_contents_by_type = {}

    for log_line in log_lines:
        if "logType" not in log_line or "contents" not in log_line:
            raise Exception(f"Invalid log line structure: {log_line}")

        log_type = log_line["logType"]
        if log_type not in log_contents_by_type:
            log_contents_by_type[log_type] = []

        log_contents_by_type[log_type].append(log_line["contents"])

    return log_contents_by_type


def get_all_log_types() -> set[str]:
    """Returns all possible log types that can be put to pod logs in agent-launcher.sh script"""
    return {
        "agent-launcher-script",
        "automation-agent",
        "automation-agent-stderr",
        "automation-agent-verbose",
        "backup-agent",
        "mongodb",
        "monitoring-agent",
        "mongodb-audit",
    }


def get_all_default_log_types() -> set[str]:
    """Returns log types that are by default enabled in pod logs in agent-launcher.sh script (normally - all without audit log)."""
    return get_all_log_types() - {"mongodb-audit"}


def assert_log_types_in_structured_json_pod_log(
    namespace: str,
    pod_name: str,
    expected_log_types: Optional[set[str]],
    api_client: Optional[kubernetes.client.ApiClient] = None,
    container_name: str = "mongodb-agent",
):
    """
    Checks pod logs if all expected_log_types are present in structured json logs.
    It fails when there are any unexpected log types in logs.
    """

    if not is_default_architecture_static():
        container_name = "mongodb-enterprise-database"

    assert expected_log_types is not None, "expected_log_types must not be None"
    pod_logs = get_structured_json_pod_logs(namespace, pod_name, container_name, api_client=api_client)

    pod_log_types = set(pod_logs.keys())
    unwanted_log_types = pod_log_types - expected_log_types
    missing_log_types = expected_log_types - pod_log_types
    assert len(unwanted_log_types) == 0, f"pod {namespace}/{pod_name} contains unwanted log types: {unwanted_log_types}"
    assert (
        len(missing_log_types) == 0
    ), f"pod {namespace}/{pod_name} doesn't contain some log types: {missing_log_types}"
