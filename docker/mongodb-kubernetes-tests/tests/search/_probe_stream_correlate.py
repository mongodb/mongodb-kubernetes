"""End-to-end stream-correlation probe.

Drives a single paging_search against the existing rs deployment while
capturing:

  - mongot pod log (Netty/gRPC HEADERS+DATA frames,
    LuceneSearchBatchProducer batches)
  - mongod pod logs (all replica-set members) with COMMAND + NETWORK
    verbosity bumped to 2 so we see every $search aggregate / getMore /
    killCursors AND every gRPC egress session lifecycle (the latter
    carries the cross-side clientId UUID).
  - envoy LB pod log with ``http2:debug``+``http:debug`` enabled, so we
    can see the per-DATA-frame visibility and pull the
    ``mongodb-clientid`` request header from each new stream.
  - the pymongo CommandListener events recorded inside this Python
    process via ``SearchConnectivityTool`` itself.

Then runs ``mongot_log_analyzer`` over each capture and prints a
unified cross-layer timeline keyed on the client_id UUID / lsid /
cursor_id join graph documented in
``tmp/search-caching-investigation/observability-followup.md``.

NOT a pytest test. Standalone investigation script.
"""

from __future__ import annotations

import os
import subprocess
import sys
import time
import urllib.request
from datetime import datetime, timezone
from pathlib import Path

from tests.common.search.connectivity import SearchConnectivityTool
from tests.common.search.mongot_log_analyzer import (
    build_stream_summaries,
    correlate_sessions_with_cursors,
    parse_client_wire_ops,
    parse_envoy_debug_log,
    print_mongod_command_report,
    print_stream_report,
    print_unified_timeline,
    read_mongod_commands,
    read_mongod_sessions,
    set_mongod_debug_logs,
    unified_timeline,
)
from tests.common.search.search_tester import SearchTester
from kubetester.kubetester import fixture as _fixture


NAMESPACE = os.getenv("NAMESPACE", "ls-0")
MDB_NAME = os.getenv("MDB_NAME", "mdb-rs-conn-tool")
MDBS_NAME = os.getenv("MDBS_NAME", "mdb-rs-conn-tool-search")
USER_NAME = os.getenv("USER_NAME", "mdb-user")
USER_PASSWORD = os.getenv("USER_PASSWORD", "mdb-user-pass")
ADMIN_USER_NAME = os.getenv("ADMIN_USER_NAME", "mdb-admin-user")
ADMIN_USER_PASSWORD = os.getenv("ADMIN_USER_PASSWORD", "mdb-admin-user-pass")

LOG_DIR = Path(os.getenv("PROBE_LOG_DIR", "/tmp/probe-stream-correlate"))
ENVOY_ADMIN_LOCAL_PORT = int(os.getenv("ENVOY_ADMIN_LOCAL_PORT", "29901"))


class _Mdb:
    name = MDB_NAME
    namespace = NAMESPACE


def pods_with_prefix(prefix: str) -> list[str]:
    out = subprocess.check_output(
        ["kubectl", "-n", NAMESPACE, "get", "pods", "-o", "name"]
    ).decode()
    return [
        line.removeprefix("pod/").strip()
        for line in out.splitlines()
        if line.startswith(f"pod/{prefix}")
    ]


def find_envoy_lb_pod() -> str | None:
    """Locate the search LB envoy pod (if managed-LB is enabled)."""
    candidates = [p for p in pods_with_prefix(MDBS_NAME) if "-lb-" in p]
    return candidates[0] if candidates else None


def start_log_tail(pod: str, target_file: Path):
    target_file.parent.mkdir(parents=True, exist_ok=True)
    f = open(target_file, "w")
    proc = subprocess.Popen(
        ["kubectl", "-n", NAMESPACE, "logs", pod, "--tail=0", "-f"],
        stdout=f, stderr=subprocess.STDOUT,
    )
    return proc, f


def start_envoy_port_forward(pod: str) -> subprocess.Popen:
    proc = subprocess.Popen(
        ["kubectl", "-n", NAMESPACE, "port-forward", pod, f"{ENVOY_ADMIN_LOCAL_PORT}:9901"],
        stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
    )
    deadline = time.monotonic() + 10
    while time.monotonic() < deadline:
        try:
            urllib.request.urlopen(
                f"http://127.0.0.1:{ENVOY_ADMIN_LOCAL_PORT}/ready", timeout=0.5
            ).read()
            return proc
        except Exception:
            time.sleep(0.2)
    proc.terminate()
    raise SystemExit("envoy port-forward did not come up")


def set_envoy_logger(paths_query: str) -> None:
    url = f"http://127.0.0.1:{ENVOY_ADMIN_LOCAL_PORT}/logging?{paths_query}"
    req = urllib.request.Request(url, method="POST")
    try:
        urllib.request.urlopen(req, timeout=5).read()
    except Exception as exc:
        print(f"[probe] envoy logger toggle failed for '{paths_query}': {exc}")


def run() -> int:
    LOG_DIR.mkdir(parents=True, exist_ok=True)

    mongot_pods = [p for p in pods_with_prefix(MDBS_NAME) if "lb-" not in p]
    mongod_pods = sorted(
        p for p in pods_with_prefix(MDB_NAME)
        if "-search-" not in p and "-mongos" not in p
    )
    envoy_pod = find_envoy_lb_pod()
    print(f"[probe] mongot pods: {mongot_pods}")
    print(f"[probe] mongod pods: {mongod_pods}")
    print(f"[probe] envoy lb pod: {envoy_pod}")

    ca = _fixture("ca-tls-full-chain.crt")
    search_tester = SearchTester.for_replicaset(_Mdb(), USER_NAME, USER_PASSWORD, use_ssl=True, ca_path=ca)
    # SearchConnectivityTool registers the CommandListener at construction.
    tool = SearchConnectivityTool(search_tester)

    # Bump mongod debug log level on EVERY member so we see $search
    # aggregates / getMores AND the gRPC egress session lifecycle
    # regardless of which node the client lands on. setParameter is
    # per-node, so direct-connect to each one.
    print("[probe] bumping mongod COMMAND+NETWORK verbosity to 2 on every member ...")
    member_clients = []
    for member_pod in mongod_pods:
        host = f"{member_pod}.{MDB_NAME}-svc.{NAMESPACE}.svc.cluster.local"
        m_tester = SearchTester(
            f"mongodb://{ADMIN_USER_NAME}:{ADMIN_USER_PASSWORD}@{host}:27017/?directConnection=true",
            use_ssl=True,
            ca_path=ca,
        )
        try:
            # Default network_level=2 surfaces id=7401401 / 7401403 (gRPC
            # egress session open/close with clientId).
            set_mongod_debug_logs(m_tester.client, command_level=2, network_level=2)
            member_clients.append(m_tester.client)
            print(f"[probe]   bumped {host}")
        except Exception as exc:
            print(f"[probe]   failed to bump {host}: {exc}")

    # Envoy port-forward + debug logger toggle (only if the LB pod exists)
    envoy_pf = None
    if envoy_pod is not None:
        try:
            envoy_pf = start_envoy_port_forward(envoy_pod)
            print("[probe] envoy: setting http2/http/router/connection to debug")
            set_envoy_logger("paths=http2:debug,http:debug,router:debug,connection:debug")
            time.sleep(0.3)
        except Exception as exc:
            print(f"[probe] envoy debug toggle setup failed: {exc}")
            envoy_pf = None

    # Start tailing
    tails: list[tuple[subprocess.Popen, object, Path]] = []
    for p in mongot_pods + mongod_pods + ([envoy_pod] if envoy_pod else []):
        path = LOG_DIR / f"{p}.log"
        proc, f = start_log_tail(p, path)
        tails.append((proc, f, path))
    time.sleep(1)

    # Anchor the in-process CommandListener events to wall-clock; we
    # don't otherwise know what time.monotonic() means in absolute terms.
    anchor_wall = datetime.now(timezone.utc)
    try:
        print("[probe] running paging_search(pages=20, batch_size=10) ...")
        pages = tool.paging_search(pages=20, interval_seconds=0.05, batch_size=10)
        ok = sum(1 for p in pages if p.success)
        wire_ops_total = sum(p.mongod_wire_ops for p in pages)
        print(
            f"[probe] done; pages_ok={ok}, docs={sum(p.returned_count for p in pages)}, "
            f"wire_ops_total={wire_ops_total}"
        )
        time.sleep(2)  # let mongot/mongod/envoy finish writing logs
    finally:
        for proc, f, _ in tails:
            proc.terminate()
            try:
                proc.wait(timeout=3)
            except subprocess.TimeoutExpired:
                proc.kill()
            f.close()

        # Restore mongod log level on every member
        print("[probe] restoring mongod log level on every member ...")
        for c in member_clients:
            try:
                set_mongod_debug_logs(c, command_level=0, network_level=0)
            except Exception as exc:
                print(f"[probe]   restore failed: {exc}")

        # Restore envoy log level + tear down port-forward
        if envoy_pf is not None:
            try:
                print("[probe] envoy: restoring log level to info")
                set_envoy_logger("level=info")
            except Exception as exc:
                print(f"[probe]   envoy restore failed: {exc}")
            envoy_pf.terminate()
            try:
                envoy_pf.wait(timeout=3)
            except subprocess.TimeoutExpired:
                envoy_pf.kill()

    # Analyze
    mongot_files = [str(LOG_DIR / f"{p}.log") for p in mongot_pods]
    mongod_files = [str(LOG_DIR / f"{p}.log") for p in mongod_pods]
    envoy_files = [str(LOG_DIR / f"{envoy_pod}.log")] if envoy_pod else []

    summaries, batches = build_stream_summaries(mongot_files, namespace=NAMESPACE)
    print_stream_report(summaries, batches)

    cmds = read_mongod_commands(mongod_files, namespace=NAMESPACE)
    print_mongod_command_report(cmds)

    sessions = read_mongod_sessions(mongod_files, namespace=NAMESPACE)
    sessions = correlate_sessions_with_cursors(sessions, cmds)
    print(f"\n=== mongod NETWORK:2 egress sessions — {len(sessions)} total ===")
    for s in sessions:
        opened = s.opened_at.isoformat() if s.opened_at else "?"
        closed = s.closed_at.isoformat() if s.closed_at else "?"
        print(
            f"  pod={s.pod}  session_id={s.session_id}  client_id={s.client_id}  "
            f"cursor_id={s.cursor_id}  status={s.status}  span={opened} → {closed}"
        )

    envoy_streams = parse_envoy_debug_log(envoy_files, namespace=NAMESPACE) if envoy_files else []
    if envoy_streams:
        print(f"\n=== envoy debug-log streams — {len(envoy_streams)} total ===")
        for es in envoy_streams:
            life = f"{es.lifetime_seconds:.2f}s" if es.lifetime_seconds is not None else "n/a"
            print(
                f"  pod={es.pod}  conn={es.connection_id}  stream={es.stream_id}  "
                f"client_id={es.client_id}  path={es.path}  status={es.grpc_status}  "
                f"rst={es.rst_stream}  bytes={es.outbound_bytes}  frames={es.outbound_data_frames}  "
                f"lifetime={life}"
            )

    # Pull every CommandListener event the tool captured during this run.
    client_records = tool.listener.snapshot_since(0)
    client_ops = parse_client_wire_ops(client_records, anchor_wall_time=anchor_wall)
    print(f"\n=== pymongo CommandListener — {len(client_ops)} event(s) ===")
    for op in client_ops[:40]:
        print(
            f"  {op.phase:<10} {op.command_name:<14} req_id={op.request_id} "
            f"conn_id={op.server_connection_id} lsid={op.lsid} cursor_id={op.cursor_id} "
            f"dur_us={op.duration_micros} n={op.n_returned}"
        )
    if len(client_ops) > 40:
        print(f"  ... ({len(client_ops) - 40} more event(s) elided)")

    timeline = unified_timeline(
        client_ops=client_ops,
        mongod_sessions=sessions,
        mongod_commands=cmds,
        envoy_streams=envoy_streams,
        mongot_streams=summaries,
        mongot_batches=batches,
    )
    print_unified_timeline(timeline, max_events=300)

    return 0


if __name__ == "__main__":
    sys.exit(run())
