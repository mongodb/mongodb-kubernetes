"""Pod-log fetching + pod discovery for the mongot log analyzer.

Two functions: ``discover_pods`` (label or name-prefix selector) and
``fetch_pod_logs`` (Python K8s client). Callers — both the test harness
and the standalone CLI — compose label selectors / name prefixes inline.

Envoy stdout access logs (always-on JSON ``upstream_host`` /
``response_flags`` records emitted by the HCM access logger) are
captured by the same ``read_namespaced_pod_log`` call — they live on
the envoy pod's stdout and need no separate fetcher.
"""

from __future__ import annotations

import os
import tempfile
from pathlib import Path
from typing import Iterable, Optional

from kubernetes import client
from kubernetes.client.rest import ApiException
from kubernetes.stream import stream
from tests import test_logger

logger = test_logger.get_test_logger(__name__)

# Canonical mongod/mongos log file inside database pods. Written by mongod
# itself via systemLog.path on a volume that survives container restarts
# (the agent log dir lives on the database pod's data PVC).
MONGOD_LOG_FILE = "/var/log/mongodb-mms-automation/mongodb.log"
# Default container to exec into for file reads on database pods. Dynamic
# arch has a single container with this name; on static arch this name
# refers to the second container, which mounts the same agent log dir as
# the first.
DEFAULT_DB_CONTAINER = "mongodb-enterprise-database"


def discover_pods(
    namespace: str,
    *,
    label_selector: Optional[str] = None,
    name_prefix: Optional[str] = None,
    api_client: Optional[client.ApiClient] = None,
) -> list[str]:
    """List pods matching ``label_selector`` OR ``name_prefix``. Sorted return.

    Exactly one of the two filters must be supplied. One function for
    every topology (RS mongot, sharded mongot per shard, mongos, envoy LB).

    ``api_client`` defaults to the central in-cluster client. Pass a
    member-cluster ``ApiClient`` to discover pods in a specific kind
    cluster (used by the MC search e2es to read per-cluster mongot
    pods that don't live on the central client).
    """
    if (label_selector is None) == (name_prefix is None):
        raise ValueError("provide exactly one of label_selector or name_prefix")
    core_v1 = client.CoreV1Api(api_client=api_client) if api_client is not None else client.CoreV1Api()
    if label_selector is not None:
        items = core_v1.list_namespaced_pod(
            namespace=namespace,
            label_selector=label_selector,
        ).items
        return sorted(p.metadata.name for p in items)
    items = core_v1.list_namespaced_pod(namespace=namespace).items
    return sorted(p.metadata.name for p in items if p.metadata.name.startswith(name_prefix))


def fetch_pod_logs(
    namespace: str,
    pods: Iterable[str],
    *,
    since_seconds: int,
    dest_dir: Optional[Path] = None,
    api_client: Optional[client.ApiClient] = None,
    container: Optional[str] = None,
) -> list[str]:
    """Fetch per-pod logs into ``dest_dir/<pod>.log``. Returns string paths.

    Uses ``CoreV1Api.read_namespaced_pod_log(since_seconds=...)``.
    Failures fetching a single pod log are logged and that pod is skipped.
    ``dest_dir`` defaults to a fresh ``tempfile.mkdtemp`` directory.
    Returns ``list[str]`` so paths feed straight into ``iter_log_lines``.

    ``api_client`` defaults to the central in-cluster client. Pass a
    member-cluster ``ApiClient`` to pull logs from pods living in a
    specific kind cluster (used by the MC search e2es for per-cluster
    mongot log attribution).

    ``container`` — when pods have multiple containers (e.g. Istio sidecar
    injection on member clusters adds ``istio-proxy`` and ``istio-validation``
    alongside the main container), the Kubernetes log API returns HTTP 400
    unless a container name is specified.  Pass the target container name to
    select the right stream; defaults to ``None`` (single-container pods).
    """
    core_v1 = client.CoreV1Api(api_client=api_client) if api_client is not None else client.CoreV1Api()
    if dest_dir is None:
        dest_dir = Path(tempfile.mkdtemp(prefix="pod-logs-"))
    else:
        dest_dir = Path(dest_dir)
        dest_dir.mkdir(parents=True, exist_ok=True)
    since = max(int(since_seconds), 1)
    paths: list[str] = []
    for pod in pods:
        try:
            kwargs: dict = dict(name=pod, namespace=namespace, since_seconds=since)
            if container is not None:
                kwargs["container"] = container
            body = core_v1.read_namespaced_pod_log(**kwargs)
        except client.exceptions.ApiException as exc:
            logger.warning(f"log fetch failed for {pod}/{container}: status={exc.status}")
            continue
        path = os.path.join(str(dest_dir), f"{pod}.log")
        with open(path, "w") as fh:
            fh.write(body or "")
        paths.append(path)
    return paths


def fetch_database_pod_logs(
    namespace: str,
    pods: Iterable[str],
    *,
    since_seconds: int,
    dest_dir: Optional[Path] = None,
    api_client: Optional[client.ApiClient] = None,
) -> list[str]:
    """Fetch mongod/mongos pod logs across both database-pod arches.

    Static-arch database pods carry 3 containers; the mongod/mongos log
    records flow through the ``mongodb-agent`` container (idx 0) which
    tails ``MDB_LOG_FILE_MONGODB`` via the agent launcher. The
    ``mongodb-enterprise-database`` container (idx 1) does its own
    ``tail -F`` but with the env var unset, so its stdout is empty.

    Dynamic-arch database pods have a single container named
    ``mongodb-enterprise-database`` running the agent launcher; the
    ``mongodb-agent`` container does not exist.

    Fetch both: on dynamic the ``mongodb-agent`` call returns 404 and
    ``fetch_pod_logs`` silently skips; on static the
    ``mongodb-enterprise-database`` call returns an empty body. Either
    way the concatenated path list carries the real records.
    """
    paths = fetch_pod_logs(
        namespace,
        pods,
        since_seconds=since_seconds,
        dest_dir=dest_dir,
        api_client=api_client,
        container="mongodb-agent",
    )
    paths.extend(
        fetch_pod_logs(
            namespace,
            pods,
            since_seconds=since_seconds,
            dest_dir=dest_dir,
            api_client=api_client,
            container="mongodb-enterprise-database",
        )
    )
    return paths


def fetch_pod_file(
    namespace: str,
    pod: str,
    path: str,
    *,
    container: str = DEFAULT_DB_CONTAINER,
    api_client: Optional[client.ApiClient] = None,
    timeout: int = 60,
) -> Optional[str]:
    """Read a single file out of a pod via the Kubernetes exec subresource.

    Uses ``connect_get_namespaced_pod_exec`` (the same call ``kubectl
    exec`` wraps) via the official Python client — no kubectl shell-out.
    Runs ``cat <path>`` in the chosen container and returns stdout as a
    string. Returns ``None`` on any failure (missing container, missing
    file, exec rejected) so callers can fall back.

    Suited for static-sized log files: the entire file is materialised
    in memory before return. For multi-megabyte mongod logs that's
    fine; for arbitrary-size streaming, prefer a tail-bounded read.
    """
    core_v1 = client.CoreV1Api(api_client=api_client) if api_client is not None else client.CoreV1Api()
    try:
        body = stream(
            core_v1.connect_get_namespaced_pod_exec,
            pod,
            namespace,
            container=container,
            command=["cat", path],
            stdout=True,
            stderr=True,
            stdin=False,
            tty=False,
            _request_timeout=timeout,
        )
    except ApiException as exc:
        logger.warning(f"fetch_pod_file({pod}/{container}:{path}) failed: status={exc.status}")
        return None
    except Exception as exc:
        logger.warning(f"fetch_pod_file({pod}/{container}:{path}) failed: {exc!r}")
        return None
    return body


def exec_in_pod(
    namespace: str,
    pod: str,
    command: list[str],
    *,
    container: Optional[str] = None,
    api_client: Optional[client.ApiClient] = None,
    timeout: int = 30,
) -> Optional[str]:
    """Run ``command`` in ``pod`` via the exec subresource; return stdout+stderr.

    Thin wrapper over ``connect_get_namespaced_pod_exec`` (the call ``kubectl
    exec`` wraps) — used by the ``boost`` CLI verb to POST to the envoy admin
    ``/logging`` endpoint from inside the envoy container (no host port-forward
    needed). Returns ``None`` on failure so callers can report-and-continue.
    Multi-container pods (Istio sidecars on member clusters) require an explicit
    ``container``.
    """
    core_v1 = client.CoreV1Api(api_client=api_client) if api_client is not None else client.CoreV1Api()
    kwargs: dict = dict(stdout=True, stderr=True, stdin=False, tty=False, _request_timeout=timeout)
    if container is not None:
        kwargs["container"] = container
    try:
        return stream(core_v1.connect_get_namespaced_pod_exec, pod, namespace, command=command, **kwargs)
    except ApiException as exc:
        logger.warning(f"exec_in_pod({pod}/{container}) failed: status={exc.status}")
        return None
    except Exception as exc:
        logger.warning(f"exec_in_pod({pod}/{container}) failed: {exc!r}")
        return None


def fetch_database_log_files(
    namespace: str,
    pods: Iterable[str],
    *,
    dest_dir: Optional[Path] = None,
    api_client: Optional[client.ApiClient] = None,
    file_path: str = MONGOD_LOG_FILE,
) -> list[str]:
    """Read the on-disk mongod/mongos log file from each database pod.

    The agent writes structured JSON records to ``file_path`` (default
    ``/var/log/mongodb-mms-automation/mongodb.log``) on a PV that
    persists across container restarts — so the captured log covers
    history that ``read_namespaced_pod_log`` would miss after a
    restart. Unlike the stdout path, the file content is raw mongod
    JSON without the launcher's ``{"logType":"mongodb","contents":...}``
    envelope; ``parse_mongod_log_line`` already handles both shapes via
    ``_unwrap_mongod_record``.

    Per-pod container fallback: tries the dynamic-arch container name
    first, then the static-arch one. The shared agent-log directory is
    mounted on both, so either succeeds in practice — the fallback
    matters only when the dynamic-arch container is missing entirely.
    Returns the list of written paths (one per pod that succeeded).
    """
    if dest_dir is None:
        dest_dir = Path(tempfile.mkdtemp(prefix="pod-files-"))
    else:
        dest_dir = Path(dest_dir)
        dest_dir.mkdir(parents=True, exist_ok=True)
    paths: list[str] = []
    for pod in pods:
        body: Optional[str] = None
        for container in (DEFAULT_DB_CONTAINER, "mongodb-agent"):
            body = fetch_pod_file(
                namespace,
                pod,
                file_path,
                container=container,
                api_client=api_client,
            )
            if body:
                break
        if body is None:
            continue
        out_path = os.path.join(str(dest_dir), f"{pod}.log")
        with open(out_path, "w") as fh:
            fh.write(body)
        paths.append(out_path)
    return paths
