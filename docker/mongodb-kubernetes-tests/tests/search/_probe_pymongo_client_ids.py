"""pymongo client-side identifiers probe.

Captures pymongo's CommandListener events + pymongo.command logger records
during a paging_search to map out what identifiers exist on the client side
that we could correlate with mongod / envoy / mongot logs.

Key questions:
  - Does pymongo expose the wire-protocol request_id per getMore?
  - Does it expose operation_id, server_connection_id, lsid?
  - Are these the same fields mongod surfaces in its COMMAND log?

NOT a pytest test.
"""

from __future__ import annotations

import logging
import os
import sys
import time
from pathlib import Path

import pymongo
import pymongo.monitoring as mon

from tests.common.search.connectivity import SearchConnectivityTool
from tests.common.search.search_tester import SearchTester
from kubetester.kubetester import fixture as _fixture

NAMESPACE = os.getenv("NAMESPACE", "ls-0")
MDB_NAME = os.getenv("MDB_NAME", "mdb-rs-conn-tool")
USER_NAME = os.getenv("USER_NAME", "mdb-user")
USER_PASSWORD = os.getenv("USER_PASSWORD", "mdb-user-pass")
OUT_DIR = Path(os.getenv("PROBE_OUT_DIR", "/tmp/probe-pymongo-client-ids"))


class _Mdb:
    name = MDB_NAME
    namespace = NAMESPACE


class CaptureCommandListener(mon.CommandListener):
    def __init__(self):
        self.records: list[dict] = []

    def started(self, event: mon.CommandStartedEvent):
        self.records.append({
            "phase": "started",
            "command_name": event.command_name,
            "database_name": event.database_name,
            "request_id": event.request_id,
            "operation_id": event.operation_id,
            "connection_id": str(event.connection_id),
            "service_id": str(event.service_id) if event.service_id else None,
            "server_connection_id": event.server_connection_id,
            "lsid": str(event.command.get("lsid")) if "lsid" in event.command else None,
            "cursor_in_cmd": event.command.get(event.command_name)
                if event.command_name in {"getMore", "killCursors"} else None,
            "timestamp": time.monotonic(),
        })

    def succeeded(self, event: mon.CommandSucceededEvent):
        self.records.append({
            "phase": "succeeded",
            "command_name": event.command_name,
            "request_id": event.request_id,
            "operation_id": event.operation_id,
            "duration_us": event.duration_micros,
            "server_connection_id": event.server_connection_id,
            "cursor_id_in_reply": (event.reply.get("cursor", {}) or {}).get("id"),
            "n_returned": len((event.reply.get("cursor", {}) or {}).get("nextBatch") or
                              (event.reply.get("cursor", {}) or {}).get("firstBatch") or []),
            "timestamp": time.monotonic(),
        })

    def failed(self, event: mon.CommandFailedEvent):
        self.records.append({
            "phase": "failed",
            "command_name": event.command_name,
            "request_id": event.request_id,
            "operation_id": event.operation_id,
            "failure": str(event.failure)[:120],
            "timestamp": time.monotonic(),
        })


class CapturePoolListener(mon.ConnectionPoolListener):
    def __init__(self):
        self.records: list[dict] = []

    def pool_created(self, event): self.records.append(("pool_created", str(event.address)))
    def pool_ready(self, event): self.records.append(("pool_ready", str(event.address)))
    def pool_cleared(self, event): self.records.append(("pool_cleared", str(event.address)))
    def pool_closed(self, event): self.records.append(("pool_closed", str(event.address)))
    def connection_created(self, event):
        self.records.append(("connection_created", str(event.address), event.connection_id))
    def connection_ready(self, event):
        self.records.append(("connection_ready", str(event.address), event.connection_id))
    def connection_closed(self, event):
        self.records.append(("connection_closed", str(event.address), event.connection_id, event.reason))
    def connection_check_out_started(self, event): pass
    def connection_check_out_failed(self, event): pass
    def connection_checked_out(self, event):
        self.records.append(("checked_out", str(event.address), event.connection_id))
    def connection_checked_in(self, event):
        self.records.append(("checked_in", str(event.address), event.connection_id))


def run() -> int:
    OUT_DIR.mkdir(parents=True, exist_ok=True)

    # Setup standard-library logging capture for pymongo.command
    cmd_log_path = OUT_DIR / "pymongo_command.log"
    handler = logging.FileHandler(cmd_log_path, mode="w")
    handler.setFormatter(logging.Formatter("%(asctime)s [%(name)s] %(message)s"))
    logger = logging.getLogger("pymongo.command")
    logger.setLevel(logging.DEBUG)
    logger.addHandler(handler)

    # Setup event listener capture
    cmd_listener = CaptureCommandListener()
    pool_listener = CapturePoolListener()

    ca = _fixture("ca-tls-full-chain.crt")
    # Re-create SearchTester so listeners are attached. We mirror what
    # SearchTester does but add event_listeners=.
    user = USER_NAME
    pwd = USER_PASSWORD
    rs_hosts = ",".join(
        f"mdb-rs-conn-tool-{i}.mdb-rs-conn-tool-svc.{NAMESPACE}.svc.cluster.local:27017"
        for i in range(3)
    )
    uri = f"mongodb://{user}:{pwd}@{rs_hosts}/?replicaSet=mdb-rs-conn-tool&tls=true&tlsCAFile={ca}"
    client = pymongo.MongoClient(uri, event_listeners=[cmd_listener, pool_listener])

    # Use directly through SearchConnectivityTool by wrapping a SearchTester
    class _ToolHolder:
        def __init__(self, c):
            self.client = c
    tool = SearchConnectivityTool(_ToolHolder(client))

    print("[probe] running paging_search(pages=5, batch_size=10)")
    pages = tool.paging_search(pages=5, interval_seconds=0.1, batch_size=10)
    ok = sum(1 for p in pages if p.success)
    docs = sum(p.returned_count for p in pages)
    print(f"[probe] done; pages_ok={ok} docs={docs}")
    time.sleep(0.5)

    # Detach
    logger.removeHandler(handler)
    handler.close()
    client.close()

    # Report
    print(f"\n=== CommandListener events ({len(cmd_listener.records)}) ===")
    by_kind: dict[str, int] = {}
    for r in cmd_listener.records:
        k = (r["phase"], r["command_name"])
        by_kind[k] = by_kind.get(k, 0) + 1
    for k, n in sorted(by_kind.items()):
        print(f"  {n:4d}× {k}")

    print("\n=== Sample started/succeeded events for getMore + aggregate ===")
    for r in cmd_listener.records:
        if r.get("command_name") in ("aggregate", "getMore", "killCursors"):
            print(" ", {k: v for k, v in r.items() if v is not None})

    print(f"\n=== ConnectionPoolListener events ({len(pool_listener.records)}) ===")
    for r in pool_listener.records[:30]:
        print(" ", r)

    print(f"\n=== pymongo.command logger output (DEBUG level, first 30 lines) ===")
    log_lines = cmd_log_path.read_text().splitlines()
    print(f"   captured {len(log_lines)} log lines (full file: {cmd_log_path})")
    for ln in log_lines[:30]:
        print("  " + ln[:200])

    return 0


if __name__ == "__main__":
    sys.exit(run())
