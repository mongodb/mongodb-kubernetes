"""Helpers shared by the e2e tests of the mongodb-proxy: the replica set test and the Ops Manager test."""

import json
import os
import re
import time
from typing import Optional

from kubetester.kubetester import KubernetesTester

PROXY_ANNOTATION = "mongodb.com/v1.mongodbProxy"
PROXY_SOCKET = "/var/run/mongodb-proxy/proxy.sock"
PROXY_ADDRESS = f"unix://{PROXY_SOCKET}"
AGENT_CONTAINER = "mongodb-agent"
DATABASE_CONTAINER = "mongodb-enterprise-database"


def proxy_image() -> Optional[str]:
    return os.environ.get("MDB_MONGODB_PROXY_IMAGE") or None


def proxy_helm_values(image: str) -> dict[str, str]:
    """The Helm values that make the chart pass `image` (registry/name:version) to the operator."""
    repository, _, version = image.rpartition(":")
    registry, _, name = repository.rpartition("/")
    return {
        "registry.mongodbProxy": registry,
        "mongodbProxy.name": name,
        "mongodbProxy.version": version,
    }


def run_operator_in_cluster(namespace: str, timeout_seconds: int = 180):
    apps = KubernetesTester.clients("appsv1")
    name = "mongodb-kubernetes-operator"
    apps.patch_namespaced_deployment_scale(name, namespace, {"spec": {"replicas": 1}})
    deadline = time.time() + timeout_seconds
    while time.time() < deadline:
        if apps.read_namespaced_deployment(name, namespace).status.ready_replicas == 1:
            return
        time.sleep(2)
    raise Exception(f"The operator did not become ready in {timeout_seconds}s")


def read_processes(pod: str, namespace: str, container: str) -> list[str]:
    """Returns the names of the processes visible in the container."""
    out = KubernetesTester.run_command_in_pod_container(
        pod,
        namespace,
        ["bash", "-c", 'for p in /proc/[0-9]*; do cat "$p/comm" 2>/dev/null; done'],
        container=container,
    )
    return [line.strip() for line in out.splitlines() if line.strip()]


def query_proxy(pod: str, namespace: str) -> dict:
    """Asks the proxy for its state from the agent container, the way the agent does it: over the socket."""
    # The status code is appended on a line of its own. The Kubernetes client turns an output that is only JSON into a
    # dict and returns its Python repr, which json.loads cannot read; the extra line keeps the output a plain string.
    out = KubernetesTester.run_command_in_pod_container(
        pod,
        namespace,
        ["curl", "-sS", "--unix-socket", PROXY_SOCKET, "-w", "\n%{http_code}", "http://proxy/v1/process"],
        container=AGENT_CONTAINER,
    )
    payload, _, status = out.rpartition("\n")
    assert status == "200", f"{pod}: the proxy answered {status}: {payload}"
    return json.loads(payload)


def query_proxy_path(pod: str, namespace: str, path: str) -> dict:
    """Calls one proxy endpoint from the agent container and returns the JSON body."""
    out = KubernetesTester.run_command_in_pod_container(
        pod,
        namespace,
        [
            "bash",
            "-c",
            "exec 3<>/dev/tcp/127.0.0.1/8099; "
            f"printf 'GET {path} HTTP/1.0\\r\\nHost: proxy\\r\\n\\r\\n' >&3; cat <&3",
        ],
        container=AGENT_CONTAINER,
    )
    head, body = out.split("\r\n\r\n", 1)
    assert re.match(r"HTTP/1\.[01] 200(?: |$)", head), head
    return json.loads(body)


def mongod_identity(pod: str, namespace: str) -> str:
    """Returns "<pid>:<start time>" of mongod in the database container, empty when it is not running.

    The proxy does not report a pid, and the pid alone can repeat after a restart, so the start time is part of it.
    """
    out = KubernetesTester.run_command_in_pod_container(
        pod,
        namespace,
        [
            "bash",
            "-c",
            'for p in /proc/[0-9]*; do if [ "$(cat "$p/comm" 2>/dev/null)" = mongod ]; then '
            'echo "${p#/proc/}:$(cut -d" " -f22 "$p/stat")"; fi; done',
        ],
        container=DATABASE_CONTAINER,
    )
    return out.strip()


def wait_until(description: str, check, timeout_seconds: int = 600, interval_seconds: int = 3):
    deadline = time.time() + timeout_seconds
    while time.time() < deadline:
        if check():
            return
        time.sleep(interval_seconds)
    raise Exception(f"Timed out after {timeout_seconds}s waiting for: {description}")


def proxy_log(pod: str, namespace: str) -> list[dict]:
    """Returns the log entries of the proxy in the current database container (the mongod lines are left out)."""
    raw = KubernetesTester.clients("corev1").read_namespaced_pod_log(pod, namespace, container=DATABASE_CONTAINER)
    entries = []
    for line in raw.splitlines():
        try:
            entry = json.loads(line)
        except ValueError:
            continue
        if entry.get("component") == "proxy":
            entries.append(entry)
    return entries


def proxy_events(pod: str, namespace: str, message: str) -> list[dict]:
    return [e for e in proxy_log(pod, namespace) if e["msg"] == message]


def restart_counts(pod: str, namespace: str) -> dict[str, int]:
    current = KubernetesTester.clients("corev1").read_namespaced_pod(pod, namespace)
    return {c.name: c.restart_count for c in current.status.container_statuses}


def wait_for_new_mongod(pod: str, namespace: str, old_identity: str, timeout_seconds: int = 600):
    """Waits until the pod runs a mongod other than old_identity and the proxy reports it running."""

    def restarted() -> bool:
        identity = mongod_identity(pod, namespace)
        return bool(identity) and identity != old_identity and query_proxy(pod, namespace)["state"] == "running"

    wait_until(f"{pod} to run a new mongod", restarted, timeout_seconds)


def post_to_proxy(pod: str, namespace: str, path: str, body: dict) -> tuple[int, dict]:
    """POSTs a JSON body to the proxy from the agent container, the way the agent does, and returns (status, body)."""
    out = KubernetesTester.run_command_in_pod_container(
        pod,
        namespace,
        [
            "curl",
            "-sS",
            "--unix-socket",
            PROXY_SOCKET,
            "-X",
            "POST",
            "-H",
            "Content-Type: application/json",
            "--data",
            json.dumps(body),
            "-w",
            "\n%{http_code}",
            f"http://proxy{path}",
        ],
        container=AGENT_CONTAINER,
    )
    payload, _, status = out.rpartition("\n")
    return int(status), json.loads(payload) if payload.strip() else {}
