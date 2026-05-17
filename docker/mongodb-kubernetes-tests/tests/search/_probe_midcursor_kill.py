"""Probe: how many pages survive from mongod's in-flight stream buffer
after mongot is killed mid-cursor?

This is the question the design doc raises — paging may keep succeeding
from local/server caches even after upstream is gone. The pymongo
``CommandListener`` only sees client-side wire ops; it cannot see
mongod's in-memory queue of streamed-from-mongot docs.
This probe measures THAT buffer directly: open a small-batch cursor,
read 1 page (forces mongot to stream a batch), kill the mongot pod,
then count how many additional pages succeed before mongod's queue
drains and surfaces the dead gRPC stream.

NOT a test. Run manually inside the devcontainer:
    PYTHONPATH=/workspace/docker/mongodb-kubernetes-tests \
        NAMESPACE=ls-0 python tests/search/_probe_midcursor_kill.py
"""

from __future__ import annotations

import os
import subprocess
import time

from tests.common.search.connectivity import SearchConnectivityTool
from tests.common.search.search_tester import SearchTester
from kubetester.kubetester import fixture as _fixture


NAMESPACE = os.getenv("NAMESPACE", "ls-0")
MDB_NAME = os.getenv("MDB_NAME", "mdb-rs-conn-tool")
MONGOT_POD = os.getenv("MONGOT_POD", "mdb-rs-conn-tool-search-0")
USER_NAME = os.getenv("USER_NAME", "mdb-user")
USER_PASSWORD = os.getenv("USER_PASSWORD", "mdb-user-pass")


class _Mdb:
    name = MDB_NAME
    namespace = NAMESPACE


def run() -> None:
    ca = _fixture("ca-tls-full-chain.crt")
    st = SearchTester.for_replicaset(_Mdb(), USER_NAME, USER_PASSWORD, use_ssl=True, ca_path=ca)
    tool = SearchConnectivityTool(st)

    print("=== Mid-cursor mongot kill: count surviving pages from mongod buffer ===")
    print("Opening cursor with batchSize=10...")
    cursor = tool.paging_cursor_open(batch_size=10)
    pre = tool.paging_cursor_read_pages(
        cursor, pages=1, interval_seconds=0, batch_size=10, first_page_index=0
    )
    print(f"pre-kill page: {pre[0]}")

    print(f"Deleting mongot pod {MONGOT_POD}...")
    subprocess.check_call(
        ["kubectl", "-n", NAMESPACE, "delete", "pod", MONGOT_POD, "--wait=false"]
    )
    time.sleep(3)  # let the kill propagate

    print("Continuing paging on SAME cursor (up to 2000 pages × 10 docs)...")
    post = tool.paging_cursor_read_pages(
        cursor,
        pages=2000,
        interval_seconds=0.0,
        batch_size=10,
        first_page_index=1,
        retry_transient_once=False,
        stop_on_error=True,
    )
    ok = sum(1 for p in post if p.success)
    errs = [p for p in post if not p.success]
    docs = sum(p.returned_count for p in post if p.success)

    print(f"surviving pages after kill: {ok}")
    print(f"docs delivered post-kill from buffer: {docs}")
    if errs:
        e = errs[0]
        head = (e.error_message or "")[:200]
        print(f"first error at page_index={e.page_index}  class={e.error_class}  failure_class={e.failure_class}")
        print(f"first error message head: {head}")
    else:
        print("no errors observed (cursor likely exhausted before mongot kill mattered)")

    try:
        cursor.close()
    except Exception as exc:
        print(f"cursor.close raised: {exc}")


if __name__ == "__main__":
    run()
