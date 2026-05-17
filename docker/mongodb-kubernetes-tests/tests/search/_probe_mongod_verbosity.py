"""Mongod verbosity probe — surface log lines from executor/network/query
components and report new log codes that fire during a paging_search.

NOT a pytest test.
"""

from __future__ import annotations

import json
import os
import subprocess
import sys
import time
from collections import Counter, defaultdict
from pathlib import Path

from tests.common.search.connectivity import SearchConnectivityTool
from tests.common.search.search_tester import SearchTester
from kubetester.kubetester import fixture as _fixture

NAMESPACE = os.getenv("NAMESPACE", "ls-0")
MDB_NAME = os.getenv("MDB_NAME", "mdb-rs-conn-tool")
MDBS_NAME = os.getenv("MDBS_NAME", "mdb-rs-conn-tool-search")
USER_NAME = os.getenv("USER_NAME", "mdb-user")
USER_PASSWORD = os.getenv("USER_PASSWORD", "mdb-user-pass")
ADMIN_USER_NAME = os.getenv("ADMIN_USER_NAME", "mdb-admin-user")
ADMIN_USER_PASSWORD = os.getenv("ADMIN_USER_PASSWORD", "mdb-admin-user-pass")
OUT_DIR = Path(os.getenv("PROBE_OUT_DIR", "/tmp/probe-mongod-verbosity"))


class _Mdb:
    name = MDB_NAME
    namespace = NAMESPACE


def pods_with_prefix(prefix: str) -> list[str]:
    out = subprocess.check_output(["kubectl", "-n", NAMESPACE, "get", "pods", "-o", "name"]).decode()
    return [line.removeprefix("pod/").strip() for line in out.splitlines() if line.startswith(f"pod/{prefix}")]


def set_log_level(client, *, executor=0, network=0, query=0, command=0, replication=0, storage=0) -> dict:
    return client.admin.command(
        "setParameter", 1,
        logComponentVerbosity={
            "executor": {"verbosity": executor},
            "network": {"verbosity": network},
            "query": {"verbosity": query},
            "command": {"verbosity": command},
            "replication": {"verbosity": replication},
            "storage": {"verbosity": storage},
        },
    )


def parse_mongod_envelope(line: str) -> dict | None:
    try:
        outer = json.loads(line)
    except Exception:
        return None
    if outer.get("logType") == "mongodb" and "contents" in outer:
        try:
            return json.loads(outer["contents"])
        except Exception:
            return None
    if "c" in outer and "msg" in outer:
        return outer
    return None


def tail_pod(pod: str, target: Path) -> tuple[subprocess.Popen, object]:
    target.parent.mkdir(parents=True, exist_ok=True)
    f = open(target, "w")
    proc = subprocess.Popen(
        ["kubectl", "-n", NAMESPACE, "logs", pod, "--tail=0", "-f"],
        stdout=f, stderr=subprocess.STDOUT,
    )
    return proc, f


def slice_log(path: Path, components: set[str]) -> list[dict]:
    out: list[dict] = []
    if not path.exists():
        return out
    for line in path.read_text().splitlines():
        rec = parse_mongod_envelope(line)
        if rec is None:
            continue
        if rec.get("c") in components:
            out.append(rec)
    return out


def summarise(records: list[dict], label: str) -> None:
    print(f"\n=== {label}: {len(records)} records ===")
    by_id_msg: Counter = Counter()
    for r in records:
        key = (r.get("id"), r.get("c"), r.get("msg", "")[:80])
        by_id_msg[key] += 1
    for (rid, comp, msg), n in by_id_msg.most_common(40):
        print(f"  {n:4d}× id={rid} c={comp} msg={msg!r}")


def example_lines(records: list[dict], filter_msg_substr: list[str], n: int = 5) -> None:
    for sub in filter_msg_substr:
        hits = [r for r in records if sub.lower() in (r.get("msg", "").lower())][:n]
        if not hits:
            continue
        print(f"\n--- examples mentioning {sub!r} ---")
        for r in hits:
            attr = r.get("attr") or {}
            ctx = ""
            for k in ("cursorId", "remoteCursorId", "ns", "namespace", "msWaitingForMongot", "host",
                      "session", "streamId", "remote", "error"):
                if k in attr:
                    ctx += f" {k}={attr[k]!r}"
            print(f"  t={r.get('t')} c={r.get('c')} id={r.get('id')} msg={r.get('msg')!r}{ctx}")


def run() -> int:
    OUT_DIR.mkdir(parents=True, exist_ok=True)
    mongod_pods = sorted(p for p in pods_with_prefix(MDB_NAME) if "-search-" not in p and "-mongos" not in p)
    print(f"[probe] mongod pods: {mongod_pods}")

    ca = _fixture("ca-tls-full-chain.crt")
    member_clients = []
    for pod in mongod_pods:
        host = f"{pod}.{MDB_NAME}-svc.{NAMESPACE}.svc.cluster.local"
        m_tester = SearchTester(
            f"mongodb://{ADMIN_USER_NAME}:{ADMIN_USER_PASSWORD}@{host}:27017/?directConnection=true",
            use_ssl=True, ca_path=ca,
        )
        try:
            set_log_level(m_tester.client, executor=2, network=2, query=2, command=2)
            member_clients.append((pod, m_tester.client))
            print(f"[probe] bumped {host}")
        except Exception as exc:
            print(f"[probe] bump failed for {host}: {exc}")

    tails = []
    for pod in mongod_pods:
        path = OUT_DIR / f"{pod}.log"
        proc, f = tail_pod(pod, path)
        tails.append((proc, f, path, pod))
    time.sleep(0.7)

    try:
        search_tester = SearchTester.for_replicaset(_Mdb(), USER_NAME, USER_PASSWORD, use_ssl=True, ca_path=ca)
        tool = SearchConnectivityTool(search_tester)
        print("[probe] running paging_search(pages=8, batch_size=12)")
        pages = tool.paging_search(pages=8, interval_seconds=0.1, batch_size=12)
        ok = sum(1 for p in pages if p.success)
        docs = sum(p.returned_count for p in pages)
        print(f"[probe] done; pages_ok={ok} docs={docs}")
        time.sleep(2)
    finally:
        for proc, f, _, _ in tails:
            proc.terminate()
            try:
                proc.wait(timeout=3)
            except subprocess.TimeoutExpired:
                proc.kill()
            f.close()
        print("\n[probe] restoring mongod log levels ...")
        for _pod, c in member_clients:
            try:
                set_log_level(c, executor=0, network=0, query=0, command=0)
            except Exception as exc:
                print(f"[probe] restore failed: {exc}")

    # Analyse
    all_records = []
    for _proc, _f, path, pod in tails:
        all_records.extend(slice_log(path, {"EXECUTOR", "NETWORK", "QUERY", "COMMAND"}))

    print(f"\n=== captured {len(all_records)} EXEC/NET/QUERY/COMMAND records ===")
    summarise([r for r in all_records if r.get("c") == "EXECUTOR"], "EXECUTOR component")
    summarise([r for r in all_records if r.get("c") == "NETWORK"], "NETWORK component")
    summarise([r for r in all_records if r.get("c") == "QUERY"], "QUERY component")
    summarise([r for r in all_records if r.get("c") == "COMMAND"], "COMMAND component (already known)")

    print("\n=== example lines mentioning cursor / mongot / batch / streaming ===")
    example_lines(all_records, ["cursor", "mongot", "batch", "stream", "egress", "gRPC"], n=3)
    return 0


if __name__ == "__main__":
    sys.exit(run())
