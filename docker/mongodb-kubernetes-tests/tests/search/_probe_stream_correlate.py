"""End-to-end stream-correlation probe.

Drives a single paging_search against the existing rs deployment while
capturing:

  - mongot pod log (the mongot side — Netty/gRPC HEADERS+DATA frames,
    LuceneSearchBatchProducer batches)
  - mongod pod logs (all replica-set members) with COMMAND verbosity
    bumped to 2 so we see every $search aggregate / getMore /
    killCursors on the wire.

Then runs ``mongot_log_analyzer`` over both captures and prints a
per-stream report plus a per-cursor mongod-command report, so we can
see what's correlatable between the two sides.

NOT a pytest test. Standalone investigation script.
"""

from __future__ import annotations

import os
import subprocess
import sys
import time
from pathlib import Path

from tests.common.search.connectivity import SearchConnectivityTool
from tests.common.search.mongot_log_analyzer import (
    build_stream_summaries,
    print_mongod_command_report,
    print_stream_report,
    read_mongod_commands,
    set_mongod_debug_logs,
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


def start_log_tail(pod: str, target_file: Path):
    target_file.parent.mkdir(parents=True, exist_ok=True)
    f = open(target_file, "w")
    proc = subprocess.Popen(
        ["kubectl", "-n", NAMESPACE, "logs", pod, "--tail=0", "-f"],
        stdout=f, stderr=subprocess.STDOUT,
    )
    return proc, f


def run() -> int:
    LOG_DIR.mkdir(parents=True, exist_ok=True)

    mongot_pods = [p for p in pods_with_prefix(MDBS_NAME) if "lb-" not in p]
    mongod_pods = sorted(
        p for p in pods_with_prefix(MDB_NAME)
        if "-search-" not in p and "-mongos" not in p
    )
    print(f"[probe] mongot pods: {mongot_pods}")
    print(f"[probe] mongod pods: {mongod_pods}")

    ca = _fixture("ca-tls-full-chain.crt")
    search_tester = SearchTester.for_replicaset(_Mdb(), USER_NAME, USER_PASSWORD, use_ssl=True, ca_path=ca)
    admin_tester = SearchTester.for_replicaset(_Mdb(), ADMIN_USER_NAME, ADMIN_USER_PASSWORD, use_ssl=True, ca_path=ca)
    tool = SearchConnectivityTool(search_tester)

    # Bump mongod debug log level on EVERY member so we see $search
    # aggregates / getMores regardless of which node the client lands on.
    # setParameter is per-node, so direct-connect to each one.
    print("[probe] bumping mongod COMMAND verbosity to 2 on every member ...")
    member_clients = []
    for member_pod in mongod_pods:
        host = f"{member_pod}.{MDB_NAME}-svc.{NAMESPACE}.svc.cluster.local"
        m_tester = SearchTester(
            f"mongodb://{ADMIN_USER_NAME}:{ADMIN_USER_PASSWORD}@{host}:27017/?directConnection=true",
            use_ssl=True,
            ca_path=ca,
        )
        try:
            set_mongod_debug_logs(m_tester.client, command_level=2, network_level=1)
            member_clients.append(m_tester.client)
            print(f"[probe]   bumped {host}")
        except Exception as exc:
            print(f"[probe]   failed to bump {host}: {exc}")

    # Start tailing
    tails: list[tuple[subprocess.Popen, Any, Path]] = []  # type: ignore[name-defined]
    for p in mongot_pods + mongod_pods:
        path = LOG_DIR / f"{p}.log"
        proc, f = start_log_tail(p, path)
        tails.append((proc, f, path))
    time.sleep(1)

    try:
        print("[probe] running paging_search(pages=20, batch_size=10) ...")
        pages = tool.paging_search(pages=20, interval_seconds=0.05, batch_size=10)
        ok = sum(1 for p in pages if p.success)
        print(f"[probe] done; pages_ok={ok}, docs={sum(p.returned_count for p in pages)}")
        time.sleep(2)  # let mongot/mongod finish writing logs
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

    # Analyze
    mongot_files = [str(LOG_DIR / f"{p}.log") for p in mongot_pods]
    mongod_files = [str(LOG_DIR / f"{p}.log") for p in mongod_pods]

    summaries, batches = build_stream_summaries(mongot_files, namespace=NAMESPACE)
    print_stream_report(summaries, batches)

    cmds = read_mongod_commands(mongod_files, namespace=NAMESPACE)
    print_mongod_command_report(cmds)

    # Cross-side correlation: align mongod aggregate-with-$search to the
    # mongot stream open whose timestamp is closest after it.
    search_cmds = [c for c in cmds if c.has_search_stage and c.command == "aggregate"]
    print(f"\n=== cross-side correlation — {len(search_cmds)} mongod $search aggregate(s) ===")
    streams_sorted = sorted(
        ((k, s) for k, s in summaries.items() if s.opened_at and s.grpc_path and "CommandService" in s.grpc_path),
        key=lambda kv: kv[1].opened_at,  # type: ignore[arg-type]
    )
    for cmd in search_cmds:
        if cmd.timestamp is None:
            continue
        # Match the first mongot stream that opened within ±5s of the mongod aggregate
        match = None
        for (pod, sid), s in streams_sorted:
            if s.opened_at is None:
                continue
            dt = abs((s.opened_at - cmd.timestamp).total_seconds())
            if dt <= 5.0:
                match = (pod, sid, s, dt)
                break
        cid = cmd.cursor_id
        if match:
            pod, sid, s, dt = match
            print(
                f"  mongod[{cmd.pod}] ns={cmd.namespace} cursorId={cid}  ←→  "
                f"mongot[{pod}] streamId={sid}  Δt={dt:.3f}s  status={s.grpc_status} rst={s.rst_stream}"
            )
        else:
            print(
                f"  mongod[{cmd.pod}] ns={cmd.namespace} cursorId={cid}  ←→  NO matching mongot stream within 5s"
            )

    return 0


if __name__ == "__main__":
    sys.exit(run())
