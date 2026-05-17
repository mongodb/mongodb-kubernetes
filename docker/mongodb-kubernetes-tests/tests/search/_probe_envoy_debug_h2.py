"""Envoy debug log capture probe.

Crank envoy's http2/connection/router loggers to debug, drive one paging_search,
capture the log slice, restore the level.

NOT a pytest test.
"""

from __future__ import annotations

import os
import subprocess
import sys
import time
import urllib.parse
import urllib.request
from pathlib import Path

from tests.common.search.connectivity import SearchConnectivityTool
from tests.common.search.search_tester import SearchTester
from kubetester.kubetester import fixture as _fixture

NAMESPACE = os.getenv("NAMESPACE", "ls-0")
MDB_NAME = os.getenv("MDB_NAME", "mdb-rs-conn-tool")
USER_NAME = os.getenv("USER_NAME", "mdb-user")
USER_PASSWORD = os.getenv("USER_PASSWORD", "mdb-user-pass")
OUT_DIR = Path(os.getenv("PROBE_OUT_DIR", "/tmp/probe-envoy-debug"))
PF_PORT = int(os.getenv("PROBE_PF_PORT", "29901"))


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


def start_pf(pod: str) -> subprocess.Popen:
    proc = subprocess.Popen(
        ["kubectl", "-n", NAMESPACE, "port-forward", pod, f"{PF_PORT}:9901"],
        stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
    )
    deadline = time.monotonic() + 10
    while time.monotonic() < deadline:
        try:
            urllib.request.urlopen(f"http://127.0.0.1:{PF_PORT}/ready", timeout=0.5).read()
            return proc
        except Exception:
            time.sleep(0.2)
    raise SystemExit("port-forward did not come up")


def set_loggers(paths: str) -> None:
    """POST /logging?paths=…   Returns full level listing if level=… is used."""
    url = f"http://127.0.0.1:{PF_PORT}/logging?{paths}"
    req = urllib.request.Request(url, method="POST")
    out = urllib.request.urlopen(req, timeout=5).read().decode()
    print(out[:400])


def start_log_tail(pod: str, target_file: Path) -> tuple[subprocess.Popen, object]:
    target_file.parent.mkdir(parents=True, exist_ok=True)
    f = open(target_file, "w")
    proc = subprocess.Popen(
        ["kubectl", "-n", NAMESPACE, "logs", pod, "--tail=0", "-f"],
        stdout=f, stderr=subprocess.STDOUT,
    )
    return proc, f


def run() -> int:
    OUT_DIR.mkdir(parents=True, exist_ok=True)
    lb_pod = find_lb_pod()
    print(f"[probe] envoy lb pod = {lb_pod}")
    pf = start_pf(lb_pod)
    try:
        print("[probe] set http2 logger to debug")
        set_loggers("paths=http2:debug,http:debug,router:debug,connection:debug")
        time.sleep(0.3)

        ca = _fixture("ca-tls-full-chain.crt")
        search_tester = SearchTester.for_replicaset(_Mdb(), USER_NAME, USER_PASSWORD, use_ssl=True, ca_path=ca)
        tool = SearchConnectivityTool(search_tester)

        log_path = OUT_DIR / f"{lb_pod}.log"
        tail_proc, tail_f = start_log_tail(lb_pod, log_path)
        time.sleep(0.5)

        print("[probe] running paging_search(pages=4, batch_size=15)")
        pages = tool.paging_search(pages=4, interval_seconds=0.2, batch_size=15)
        ok = sum(1 for p in pages if p.success)
        docs = sum(p.returned_count for p in pages)
        print(f"[probe] done; pages_ok={ok} docs={docs}")
        time.sleep(1)

        tail_proc.terminate()
        try:
            tail_proc.wait(timeout=3)
        except subprocess.TimeoutExpired:
            tail_proc.kill()
        tail_f.close()
    finally:
        print("\n[probe] restoring envoy log level to info")
        set_loggers("level=info")
        pf.terminate()
        try:
            pf.wait(timeout=3)
        except subprocess.TimeoutExpired:
            pf.kill()

    # Show a clean slice
    lines = log_path.read_text().splitlines()
    print(f"\n[probe] captured {len(lines)} log lines in {log_path}")
    # Filter to lines that look interesting for stream/frame-level reasoning
    interesting = [
        ln for ln in lines
        if any(kw in ln for kw in ("DATA", "HEADERS", "stream", "STREAM", "RST", "trailers", "grpc"))
    ]
    print(f"[probe] {len(interesting)} lines mentioning stream/data/headers")
    sample = OUT_DIR / "interesting_slice.log"
    sample.write_text("\n".join(interesting))
    print(f"[probe] wrote slice to {sample}")
    # Print first 50 of those
    for ln in interesting[:80]:
        print(ln)
    return 0


if __name__ == "__main__":
    sys.exit(run())
