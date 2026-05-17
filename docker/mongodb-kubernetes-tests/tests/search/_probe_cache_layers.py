"""Empirical probe: where (if anywhere) does paging-search cache?

NOT a pytest test. Standalone script run inside the devcontainer that
drives ``paging_search`` against the existing ``mdb-rs-conn-tool``
deployment and snapshots counters at three layers between calls:

  1. pymongo client buffer            (per-page mongod_wire_ops counter)
  2. mongod serverStatus().metrics    (aggregate / getMore / killCursors)
  3. envoy admin /stats               (upstream_rq_completed to mongot)

Run inside devcontainer:
    cd /workspace/docker/mongodb-kubernetes-tests
    NAMESPACE=ls-0 python tests/search/_probe_cache_layers.py
"""

from __future__ import annotations

import json
import os
import subprocess
import sys
import time
from contextlib import contextmanager
from typing import Any

import requests
from tests import test_logger
from tests.common.search.connectivity import SearchConnectivityTool

# Reuse the operational SearchTester from the e2e tests.
from tests.common.search.search_tester import SearchTester


NAMESPACE = os.getenv("NAMESPACE", "ls-0")
MDB_NAME = os.getenv("MDB_NAME", "mdb-rs-conn-tool")
MDBS_NAME = os.getenv("MDBS_NAME", "mdb-rs-conn-tool-search")
USER_NAME = os.getenv("USER_NAME", "mdb-user")
USER_PASSWORD = os.getenv("USER_PASSWORD", "mdb-user-pass")
ADMIN_USER_NAME = os.getenv("ADMIN_USER_NAME", "mdb-admin-user")
ADMIN_USER_PASSWORD = os.getenv("ADMIN_USER_PASSWORD", "mdb-admin-user-pass")
ENVOY_ADMIN_LOCAL_PORT = int(os.getenv("ENVOY_ADMIN_LOCAL_PORT", "19901"))

logger = test_logger.get_test_logger(__name__)


# ----------------------------------------------------------------------
# Layer probes
# ----------------------------------------------------------------------


def find_envoy_pod() -> str:
    """Locate the envoy LB pod via kubectl (avoids Python k8s client + proxy-url issues)."""
    out = subprocess.check_output(
        [
            "kubectl",
            "-n",
            NAMESPACE,
            "get",
            "pods",
            "-l",
            f"mongodbsearch={MDBS_NAME}",
            "-o",
            "jsonpath={range .items[?(@.status.phase==\"Running\")]}{.metadata.name}{\"\\n\"}{end}",
        ]
    ).decode()
    for line in out.splitlines():
        if f"{MDBS_NAME}-lb-" in line:
            return line.strip()
    # Fallback: scan all pods in the namespace
    out = subprocess.check_output(
        ["kubectl", "-n", NAMESPACE, "get", "pods", "-o", "name"]
    ).decode()
    for line in out.splitlines():
        name = line.removeprefix("pod/").strip()
        if name.startswith(f"{MDBS_NAME}-lb-"):
            return name
    raise RuntimeError(f"no envoy LB pod found in {NAMESPACE} matching {MDBS_NAME}-lb-*")


@contextmanager
def envoy_admin_port_forward(pod_name: str):
    """Spawn `kubectl port-forward` and yield the local port URL.

    Uses the in-container kubectl + KUBECONFIG, so this works inside the
    devcontainer when devenv has been sourced.
    """
    proc = subprocess.Popen(
        [
            "kubectl",
            "-n",
            NAMESPACE,
            "port-forward",
            pod_name,
            f"{ENVOY_ADMIN_LOCAL_PORT}:9901",
        ],
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
    )
    # Wait for the local port to accept
    deadline = time.monotonic() + 10.0
    while time.monotonic() < deadline:
        try:
            r = requests.get(f"http://127.0.0.1:{ENVOY_ADMIN_LOCAL_PORT}/ready", timeout=0.5)
            if r.status_code == 200:
                break
        except requests.RequestException:
            time.sleep(0.2)
    else:
        proc.terminate()
        raise RuntimeError("envoy port-forward did not become ready")
    try:
        yield f"http://127.0.0.1:{ENVOY_ADMIN_LOCAL_PORT}"
    finally:
        proc.terminate()
        try:
            proc.wait(timeout=3)
        except subprocess.TimeoutExpired:
            proc.kill()


def envoy_snapshot(base: str) -> dict[str, int]:
    """Pull mongot_rs_cluster counters from envoy admin."""
    r = requests.get(
        f"{base}/stats",
        params={"filter": "mongot_rs_cluster", "format": "json"},
        timeout=5,
    )
    r.raise_for_status()
    payload = r.json()
    out: dict[str, int] = {}
    for entry in payload.get("stats", []):
        # entry: {"name": ..., "value": N} for counters/gauges
        if "name" in entry and "value" in entry:
            out[entry["name"]] = entry["value"]
    return out


def mongod_snapshot(client) -> dict[str, int]:
    """Pull server-side command counters via serverStatus()."""
    status = client.admin.command("serverStatus")
    cmds = status.get("metrics", {}).get("commands", {})

    def get(name: str) -> int:
        v = cmds.get(name, {})
        return int(v.get("total", 0)) if isinstance(v, dict) else 0

    return {
        "aggregate.total": get("aggregate"),
        "getMore.total": get("getMore"),
        "killCursors.total": get("killCursors"),
    }


# ----------------------------------------------------------------------
# Reporting helpers
# ----------------------------------------------------------------------


def diff_counters(before: dict[str, int], after: dict[str, int], keys: list[str]) -> dict[str, int]:
    return {k: after.get(k, 0) - before.get(k, 0) for k in keys}


def page_summary(pages: list) -> dict[str, Any]:
    n = len(pages)
    return {
        "pages_total": n,
        "pages_success": sum(1 for p in pages if p.success),
        "pages_failed": sum(1 for p in pages if not p.success),
        "pages_buffer_only": sum(1 for p in pages if p.success and p.mongod_wire_ops == 0),
        "pages_hit_mongod": sum(1 for p in pages if p.success and p.mongod_wire_ops > 0),
        "wire_ops_total": sum(p.mongod_wire_ops for p in pages),
        "total_docs_returned": sum(p.returned_count for p in pages),
    }


def banner(text: str) -> None:
    print(f"\n{'=' * 70}\n{text}\n{'=' * 70}")


def show(label: str, value: Any) -> None:
    print(f"  {label:50s} {value}")


# ----------------------------------------------------------------------
# Scenarios
# ----------------------------------------------------------------------


def run_scenario(
    name: str,
    tool: SearchConnectivityTool,
    mongo_client,
    envoy_base: str,
    *,
    pages: int,
    batch_size: int,
    interval_seconds: float = 0.05,
) -> None:
    banner(f"SCENARIO: {name}  (pages={pages} batch_size={batch_size})")

    envoy_before = envoy_snapshot(envoy_base)
    mongod_before = mongod_snapshot(mongo_client)
    t0 = time.monotonic()
    page_results = tool.paging_search(
        pages=pages,
        interval_seconds=interval_seconds,
        batch_size=batch_size,
    )
    elapsed = time.monotonic() - t0
    # Allow envoy/mongod counters to finalize (close frames, etc).
    time.sleep(0.5)
    envoy_after = envoy_snapshot(envoy_base)
    mongod_after = mongod_snapshot(mongo_client)

    summary = page_summary(page_results)
    show("elapsed_seconds", f"{elapsed:.2f}")
    for k, v in summary.items():
        show(k, v)

    # mongod-side
    mongod_keys = ["aggregate.total", "getMore.total", "killCursors.total"]
    mongod_delta = diff_counters(mongod_before, mongod_after, mongod_keys)
    print("  mongod serverStatus().metrics.commands deltas:")
    for k, v in mongod_delta.items():
        show(f"  Δ {k}", v)

    # envoy-side: relevant subset
    envoy_keys = [
        "cluster.mongot_rs_cluster.upstream_rq_total",
        "cluster.mongot_rs_cluster.upstream_rq_completed",
        "cluster.mongot_rs_cluster.upstream_rq_active",
        "cluster.mongot_rs_cluster.upstream_cx_total",
        "cluster.mongot_rs_cluster.upstream_cx_http2_total",
        "cluster.mongot_rs_cluster.external.upstream_rq_200",
        "cluster.mongot_rs_cluster.external.upstream_rq_completed",
        "cluster.mongot_rs_cluster.http2.streams_active",
        "cluster.mongot_rs_cluster.default.total_match_count",
    ]
    envoy_delta = diff_counters(envoy_before, envoy_after, envoy_keys)
    print("  envoy /stats deltas (mongot_rs_cluster):")
    for k, v in envoy_delta.items():
        show(f"  Δ {k.replace('cluster.mongot_rs_cluster.', '')}", v)

    # Per-page timing — print first 5, last 3
    print("  page detail (first 5, last 3):")
    for p in page_results[:5]:
        print(f"    {p}")
    if len(page_results) > 8:
        print("    ...")
    for p in page_results[-3:]:
        print(f"    {p}")


# ----------------------------------------------------------------------
# Main
# ----------------------------------------------------------------------


def main() -> int:
    envoy_pod = find_envoy_pod()
    print(f"[probe] envoy LB pod: {envoy_pod}")

    # Build a search tester pointed at the deployment.
    # We need a stand-in for `mdb` that has .name and .namespace.
    class _Mdb:
        def __init__(self, name: str, namespace: str) -> None:
            self.name = name
            self.namespace = namespace

    mdb_stub = _Mdb(MDB_NAME, NAMESPACE)
    from kubetester.kubetester import fixture as _fixture  # noqa: WPS433
    ca_path = os.environ.get("ISSUER_CA_FILEPATH") or _fixture("ca-tls-full-chain.crt")
    # SearchTester.for_replicaset doesn't strictly require the file to exist
    # when use_ssl=True if the env already has system certs; we'll try ssl=True
    # first and fall back. Actually: the deployment uses TLS, so we MUST use SSL.
    # Search uses the regular user; serverStatus needs the admin user (clusterMonitor role).
    search_tester = SearchTester.for_replicaset(mdb_stub, USER_NAME, USER_PASSWORD, use_ssl=True, ca_path=ca_path)
    admin_tester = SearchTester.for_replicaset(
        mdb_stub, ADMIN_USER_NAME, ADMIN_USER_PASSWORD, use_ssl=True, ca_path=ca_path
    )
    tool = SearchConnectivityTool(search_tester)
    mongo_client = admin_tester.client

    # warm: ensure we can ping
    try:
        mongo_client.admin.command("ping")
    except Exception as exc:
        print(f"[probe] mongod ping failed: {exc}", file=sys.stderr)
        return 1
    print("[probe] mongod ping OK")

    with envoy_admin_port_forward(envoy_pod) as envoy_base:
        # baseline
        e0 = envoy_snapshot(envoy_base)
        m0 = mongod_snapshot(mongo_client)
        print(f"[probe] envoy baseline upstream_rq_completed = "
              f"{e0.get('cluster.mongot_rs_cluster.upstream_rq_completed')}")
        print(f"[probe] mongod baseline aggregate/getMore/killCursors = "
              f"{m0}")

        # 1. Few pages, batch_size MATCHES cursor batchSize.  This is the
        #    test_paging_search_first_page_is_upstream2 shape.
        run_scenario("3 pages × batch_size=20", tool, mongo_client, envoy_base,
                     pages=3, batch_size=20)

        run_scenario("10 pages × batch_size=20 (the test the user ran)",
                     tool, mongo_client, envoy_base,
                     pages=10, batch_size=20)

        # 2. Many pages — does upstream_rq_completed stay at +1 ?
        run_scenario("50 pages × batch_size=20", tool, mongo_client, envoy_base,
                     pages=50, batch_size=20)

        # 3. Same total docs but smaller batch_size → MANY more getMores
        run_scenario("100 pages × batch_size=1 (1-doc-per-page)",
                     tool, mongo_client, envoy_base,
                     pages=100, batch_size=1)

        # 4. Single large page — should be 1 aggregate, maybe 0 or 1 getMore
        run_scenario("1 page × batch_size=200", tool, mongo_client, envoy_base,
                     pages=1, batch_size=200)

        # 5. Repeated identical paging_search calls — each opens a fresh cursor,
        #    so envoy should see N new upstream requests (N = number of calls).
        banner("SCENARIO: 5 sequential paging_search calls (each pages=3 batch=20)")
        envoy_before = envoy_snapshot(envoy_base)
        mongod_before = mongod_snapshot(mongo_client)
        for i in range(5):
            tool.paging_search(pages=3, interval_seconds=0.05, batch_size=20)
        time.sleep(0.5)
        envoy_after = envoy_snapshot(envoy_base)
        mongod_after = mongod_snapshot(mongo_client)
        show("Δ envoy upstream_rq_completed",
             envoy_after.get("cluster.mongot_rs_cluster.upstream_rq_completed", 0)
             - envoy_before.get("cluster.mongot_rs_cluster.upstream_rq_completed", 0))
        show("Δ mongod aggregate.total",
             mongod_after["aggregate.total"] - mongod_before["aggregate.total"])
        show("Δ mongod getMore.total",
             mongod_after["getMore.total"] - mongod_before["getMore.total"])

    return 0


if __name__ == "__main__":
    sys.exit(main())
