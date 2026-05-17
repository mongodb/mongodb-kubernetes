"""Envoy admin /stats Δ probe for per-mongot-pull signal.

Goal: find an envoy admin-stats counter that ticks per LuceneSearchBatchProducer
event (i.e. per mongot batch pull on the gRPC stream), so we can Δ before/after
and back-derive how many distinct mongot pulls happened.

Approach: snapshot all mongot_rs_cluster stats (and a separate snapshot of the
matching cluster.* tree) before/after a paging_search of known shape, compute
deltas, write to /tmp.

NOT a pytest test. Standalone investigation script.
"""

from __future__ import annotations

import os
import subprocess
import sys
import time
from pathlib import Path

from tests.common.search.connectivity import SearchConnectivityTool
from tests.common.search.search_tester import SearchTester
from kubetester.kubetester import fixture as _fixture

NAMESPACE = os.getenv("NAMESPACE", "ls-0")
MDB_NAME = os.getenv("MDB_NAME", "mdb-rs-conn-tool")
USER_NAME = os.getenv("USER_NAME", "mdb-user")
USER_PASSWORD = os.getenv("USER_PASSWORD", "mdb-user-pass")
OUT_DIR = Path(os.getenv("PROBE_OUT_DIR", "/tmp/probe-envoy-stats"))


class _Mdb:
    name = MDB_NAME
    namespace = NAMESPACE


def find_lb_pod() -> str:
    out = subprocess.check_output(
        ["kubectl", "-n", NAMESPACE, "get", "pods", "-o", "name"]
    ).decode()
    for line in out.splitlines():
        if "-search-lb-" in line:
            return line.removeprefix("pod/").strip()
    raise SystemExit("No -search-lb- pod found")


_PF_PORT = int(os.getenv("PROBE_PF_PORT", "29901"))


def start_pf(pod: str) -> subprocess.Popen:
    proc = subprocess.Popen(
        ["kubectl", "-n", NAMESPACE, "port-forward", pod, f"{_PF_PORT}:9901"],
        stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
    )
    # Wait for the port to be ready
    import urllib.request
    deadline = time.monotonic() + 10
    while time.monotonic() < deadline:
        try:
            urllib.request.urlopen(f"http://127.0.0.1:{_PF_PORT}/ready", timeout=0.5).read()
            return proc
        except Exception:
            time.sleep(0.2)
    raise SystemExit("port-forward did not come up")


def snapshot(pod: str, label: str) -> dict[str, Path]:
    """Capture three stat slices via in-devc port-forward -> envoy admin."""
    import urllib.request
    OUT_DIR.mkdir(parents=True, exist_ok=True)
    slices = {
        "cluster_mongot": "filter=mongot_rs_cluster",
        "http2":          "filter=http2",
        "all":            "",
    }
    paths = {}
    for name, q in slices.items():
        url = f"http://127.0.0.1:{_PF_PORT}/stats" + (f"?{q}&format=text" if q else "?format=text")
        out = urllib.request.urlopen(url, timeout=10).read().decode()
        p = OUT_DIR / f"{label}-{name}.txt"
        p.write_text(out)
        paths[name] = p
    return paths


def diff_counters(snap_before: Path, snap_after: Path) -> list[tuple[str, int, int, int]]:
    """Return [(metric, before, after, delta)] sorted by abs(delta) desc."""
    def read(p: Path) -> dict[str, int]:
        d = {}
        for line in p.read_text().splitlines():
            if ":" not in line:
                continue
            k, _, v = line.partition(":")
            k = k.strip()
            v = v.strip()
            try:
                d[k] = int(v)
            except ValueError:
                continue
        return d
    b = read(snap_before)
    a = read(snap_after)
    keys = set(b) | set(a)
    rows = []
    for k in keys:
        bv = b.get(k, 0)
        av = a.get(k, 0)
        delta = av - bv
        if delta != 0:
            rows.append((k, bv, av, delta))
    rows.sort(key=lambda x: abs(x[3]), reverse=True)
    return rows


def run() -> int:
    OUT_DIR.mkdir(parents=True, exist_ok=True)
    lb_pod = find_lb_pod()
    print(f"[probe] envoy lb pod = {lb_pod}")
    pf = start_pf(lb_pod)
    print(f"[probe] port-forward active on 127.0.0.1:{_PF_PORT}")

    ca = _fixture("ca-tls-full-chain.crt")
    search_tester = SearchTester.for_replicaset(_Mdb(), USER_NAME, USER_PASSWORD, use_ssl=True, ca_path=ca)
    tool = SearchConnectivityTool(search_tester)

    print("[probe] snapshot BEFORE")
    snap_before = snapshot(lb_pod, "before")
    time.sleep(0.5)

    print("[probe] running paging_search(pages=20, batch_size=10)")
    pages = tool.paging_search(pages=20, interval_seconds=0.05, batch_size=10)
    ok = sum(1 for p in pages if p.success)
    docs = sum(p.returned_count for p in pages)
    print(f"[probe] done; pages_ok={ok} docs={docs}")
    time.sleep(1)

    print("[probe] snapshot AFTER")
    snap_after = snapshot(lb_pod, "after")

    # Now do a second run with different shape to triangulate per-pull vs
    # per-stream counters.
    print("\n[probe] snapshot MID")
    snap_mid = snapshot(lb_pod, "mid")
    print("[probe] running paging_search(pages=5, batch_size=200)  -- bigger pages, expect fewer mongot pulls")
    pages2 = tool.paging_search(pages=5, interval_seconds=0.05, batch_size=200)
    ok2 = sum(1 for p in pages2 if p.success)
    docs2 = sum(p.returned_count for p in pages2)
    print(f"[probe] done; pages_ok={ok2} docs={docs2}")
    time.sleep(1)
    snap_post2 = snapshot(lb_pod, "post2")

    pf.terminate()
    try:
        pf.wait(timeout=3)
    except subprocess.TimeoutExpired:
        pf.kill()

    for name in ("cluster_mongot", "http2", "all"):
        print(f"\n========= delta for {name} (run1: 20 × 10 = 200 docs) =========")
        rows = diff_counters(snap_before[name], snap_after[name])
        for k, bv, av, d in rows[:50]:
            print(f"  Δ={d:+8d}  {k}  ({bv} → {av})")
        print(f"\n========= delta for {name} (run2: 5 × 200 = 1000 docs)  =========")
        rows = diff_counters(snap_mid[name], snap_post2[name])
        for k, bv, av, d in rows[:50]:
            print(f"  Δ={d:+8d}  {k}  ({bv} → {av})")

    return 0


if __name__ == "__main__":
    sys.exit(run())
