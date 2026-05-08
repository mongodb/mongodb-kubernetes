"""Capture one analyzer-fixture scenario: client wire ops + pod logs.

Runs INSIDE the devcontainer (in-cluster DNS) and writes to
``tests/common/search/testdata/fixtures/<scenario>/``.

What it produces per scenario:
- ``<pod>.log`` one per pod across mongos / mongod / mongot / envoy
- ``client_wire_ops.jsonl`` one record per pymongo CommandListener event
- ``metadata.json`` namespace, names, topology, pages, fault info, timestamps

Usage (from devc, with kubeconfig already on PATH):

  python -m tests.common.search.testdata.capture_client_wire_ops \\
      --scenario rs-paging-clean \\
      --namespace ls-25 --mdb-name mdb-rs-conn-tool --topology rs \\
      --pages 5 --batch-size 10

  python -m tests.common.search.testdata.capture_client_wire_ops \\
      --scenario sharded-paging-clean \\
      --namespace ls-25 --mdb-name mdb-sh-conn-tool --topology sharded \\
      --pages 5 --batch-size 10

Reading the saved JSONL back (e.g. when bootstrapping the SQLite test):

  with open("client_wire_ops.jsonl") as fh:
      records = [json.loads(line) for line in fh]
"""

from __future__ import annotations

import argparse
import json
import os
import pathlib
import sys
import time
from dataclasses import asdict
from datetime import datetime, timezone
from typing import Optional

# Reuse existing helpers
from tests.common.search.connectivity import SearchConnectivityTool
from tests.common.search.log_analyzer.analyzer import (
    ClientWireOp,
    CommandEventListener,
    set_mongod_debug_logs,
    set_mongos_debug_logs,
)
from tests.common.search.rs_search_helper import get_rs_search_tester
from tests.common.search.sharded_search_helper import get_search_tester

ADMIN_USER = os.environ.get("MCK_ADMIN_USER", "mdb-admin-user")
ADMIN_PASS = os.environ.get("MCK_ADMIN_PASS", "mdb-admin-user-pass")


# ---------------------------------------------------------------------------
# Kubernetes helpers (python kubernetes client; kubeconfig loaded lazily so
# importing this module from non-cluster contexts does not error)
# ---------------------------------------------------------------------------


def _core_v1():
    from kubernetes import client, config

    try:
        config.load_kube_config()
    except Exception:
        config.load_incluster_config()
    return client.CoreV1Api()


def _custom_objects():
    from kubernetes import client, config

    try:
        config.load_kube_config()
    except Exception:
        config.load_incluster_config()
    return client.CustomObjectsApi()


def list_pods(namespace: str) -> list[str]:
    pods = _core_v1().list_namespaced_pod(namespace=namespace).items
    return [p.metadata.name for p in pods]


def get_shard_count(namespace: str, mdb_name: str) -> int:
    obj = _custom_objects().get_namespaced_custom_object(
        group="mongodb.com",
        version="v1",
        namespace=namespace,
        plural="mongodb",
        name=mdb_name,
    )
    return int(obj.get("spec", {}).get("shardCount") or 0)


def fetch_pod_log(namespace: str, pod: str, since_seconds: int, out_path: pathlib.Path) -> None:
    """Read pod log via the kubernetes API; best-effort, logs failures."""
    try:
        body = _core_v1().read_namespaced_pod_log(
            name=pod,
            namespace=namespace,
            since_seconds=since_seconds,
            _preload_content=True,
        )
    except Exception as exc:
        sys.stderr.write(f"# log fetch failed for {pod}: {exc!r}\n")
        return
    out_path.write_text(body or "")


def delete_pod(namespace: str, pod: str) -> None:
    """Hard-kill (no grace period) so the disruption is sharp."""
    from kubernetes import client as _k8s_client

    try:
        _core_v1().delete_namespaced_pod(
            name=pod,
            namespace=namespace,
            body=_k8s_client.V1DeleteOptions(grace_period_seconds=0),
        )
    except Exception as exc:
        sys.stderr.write(f"# delete_pod failed for {pod}: {exc!r}\n")


# ---------------------------------------------------------------------------
# Pod-set discovery (mirrors log_analyzer_cli._pod_groups but standalone)
# ---------------------------------------------------------------------------


def discover_pods(namespace: str, mdb_name: str, mdbs_name: str, topology: str) -> dict[str, list[str]]:
    pods = list_pods(namespace)
    import re

    lb_prefix = f"{mdbs_name}-search-lb-0-0-"
    envoy = sorted(p for p in pods if p.startswith(lb_prefix))
    if topology == "sharded":
        mongos = sorted(p for p in pods if p.startswith(f"{mdb_name}-mongos-"))
        shard_re = re.compile(rf"^{re.escape(mdb_name)}-(\d+)-\d+$")
        mongod = sorted(p for p in pods if shard_re.match(p))
        mongot = sorted(
            p for p in pods if p.startswith(f"{mdbs_name}-search-0-{mdb_name}-") and not p.startswith(lb_prefix)
        )
        return {"mongos": mongos, "mongod": mongod, "mongot": mongot, "envoy": envoy}
    # RS
    rs_re = re.compile(rf"^{re.escape(mdb_name)}-\d+$")
    mongod = sorted(p for p in pods if rs_re.match(p))
    mongot_prefix = f"{mdbs_name}-search-"
    mongot = sorted(p for p in pods if p.startswith(mongot_prefix) and not p.startswith(lb_prefix))
    return {"mongos": [], "mongod": mongod, "mongot": mongot, "envoy": envoy}


# ---------------------------------------------------------------------------
# Search tester factory (minimal — mirrors the e2e helpers)
# ---------------------------------------------------------------------------


def make_tool(namespace: str, mdb_name: str, topology: str) -> SearchConnectivityTool:
    # Load the live MongoDB CR via the kubetester wrapper instead of a
    # local stand-in. Downstream helpers (SearchTester.for_replicaset /
    # for_sharded) only read .name/.namespace, but using the real type
    # keeps this script consistent with the e2e helpers and lets it
    # access spec/status if future scenarios need it.
    from kubetester.mongodb import MongoDB

    mdb = MongoDB(name=mdb_name, namespace=namespace).load()
    if topology == "sharded":
        st = get_search_tester(mdb, ADMIN_USER, ADMIN_PASS, use_ssl=True)
    else:
        st = get_rs_search_tester(mdb, ADMIN_USER, ADMIN_PASS, use_ssl=True)
    return SearchConnectivityTool(st)


# ---------------------------------------------------------------------------
# Capture orchestration
# ---------------------------------------------------------------------------


def serialise_wire_op(op: ClientWireOp, monotonic_to_wall: float) -> dict:
    """JSON-able shape; reconstruct wall-clock ts from monotonic + anchor."""
    d = asdict(op)
    # Replace monotonic ``timestamp`` with both monotonic + wall-clock ISO.
    mono = d.pop("timestamp")
    d["monotonic"] = mono
    d["timestamp"] = datetime.fromtimestamp(monotonic_to_wall + mono, timezone.utc).isoformat()
    return d


def run_capture(args: argparse.Namespace) -> int:
    out_root = pathlib.Path(__file__).resolve().parent / "fixtures" / args.scenario
    out_root.mkdir(parents=True, exist_ok=True)

    # Resolve names + topology.
    namespace = args.namespace
    mdb_name = args.mdb_name
    mdbs_name = args.mdbs_name or args.mdb_name
    topology = args.topology

    print(f"# scenario={args.scenario} ns={namespace} mdb={mdb_name} mdbs={mdbs_name} topology={topology}")

    # Mark wall-clock anchor BEFORE opening the cursor so log --since covers
    # the full window with margin.
    capture_started_wall = datetime.now(timezone.utc)
    capture_started_mono = time.monotonic()
    monotonic_to_wall = capture_started_wall.timestamp() - capture_started_mono

    # Drive the actual workload.
    # Attach a CommandEventListener to the pymongo client BEFORE creating the
    # tool so that all wire-op events are captured. The listener is kept
    # separate from the connectivity tool — the tool handles query execution
    # only; the log analyzer captures the wire events independently.
    wire_listener = CommandEventListener()
    tool = make_tool(namespace, mdb_name, topology)
    # Register the listener on the pymongo client via the event_listeners list.
    # pymongo supports attaching listeners after construction via the internal
    # event system. As a practical alternative we register it on the client's
    # _event_listeners list if available; otherwise we leave it empty and the
    # capture script will produce an empty client_wire_ops.jsonl.
    try:
        tool.search_tester.client._event_listeners.append(wire_listener)  # type: ignore[attr-defined]
    except (AttributeError, TypeError):
        pass  # pymongo version may differ; wire ops will be empty
    pages_read = 0
    verbosity_was_bumped = False
    shard_clients: list = []  # per-shard mongod testers for sharded mode

    try:
        # Bump server-side verbosity so the analyzer's COMMAND/NETWORK
        # joins have something to chew on. RS: hits the primary mongod
        # via the cursor's MongoClient. Sharded: hits mongos AND each
        # shard's primary mongod via directConnection (mirrors what the
        # e2e ``search_connectivity_tool_sharded`` test does, see the
        # ``get_shard_mongod_tester`` calls there).
        try:
            if topology == "sharded":
                from tests.common.search.sharded_search_helper import get_shard_mongod_tester

                set_mongos_debug_logs(tool.search_tester.client, command_level=2, network_level=2)
                from kubetester.mongodb import MongoDB

                mdb = MongoDB(name=mdb_name, namespace=namespace).load()
                spec = mdb["spec"] if "spec" in mdb else {}
                shard_count = int(spec.get("shardCount") or get_shard_count(namespace, mdb_name))
                for shard_index in range(shard_count):
                    shard_tester = get_shard_mongod_tester(
                        mdb,
                        shard_index,
                        member_index=0,
                        username=ADMIN_USER,
                        password=ADMIN_PASS,
                        use_ssl=True,
                    )
                    set_mongod_debug_logs(shard_tester.client, command_level=2, network_level=2)
                    shard_clients.append(shard_tester)
            else:
                set_mongod_debug_logs(tool.search_tester.client, command_level=2, network_level=2)
            verbosity_was_bumped = True
        except Exception as exc:
            sys.stderr.write(f"# verbosity bump failed (continuing anyway): {exc}\n")
        marker = wire_listener.current_marker()
        tool.paging_search(
            pages=args.pages,
            interval_seconds=args.interval_seconds,
            batch_size=args.batch_size,
        )
        pages_read = args.pages
        records = wire_listener.snapshot_since(marker)
    finally:
        if verbosity_was_bumped:
            try:
                if topology == "sharded":
                    set_mongos_debug_logs(tool.search_tester.client, command_level=0, network_level=0)
                    for shard_tester in shard_clients:
                        try:
                            set_mongod_debug_logs(shard_tester.client, command_level=0, network_level=0)
                        except Exception as exc:
                            sys.stderr.write(f"# per-shard verbosity restore failed: {exc}\n")
                else:
                    set_mongod_debug_logs(tool.search_tester.client, command_level=0, network_level=0)
            except Exception as exc:
                sys.stderr.write(f"# verbosity restore failed: {exc}\n")
        for shard_tester in shard_clients:
            try:
                shard_tester.client.close()
            except Exception:
                pass
        try:
            tool.search_tester.client.close()
        except Exception:
            pass

    # Wait for log buffering then fetch logs.
    if args.post_settle_seconds > 0:
        print(f"# settling {args.post_settle_seconds}s for log shipping")
        time.sleep(args.post_settle_seconds)

    elapsed = (datetime.now(timezone.utc) - capture_started_wall).total_seconds()
    since_seconds = int(elapsed) + args.log_padding_seconds
    print(f"# fetching pod logs (--since={since_seconds}s)")

    groups = discover_pods(namespace, mdb_name, mdbs_name, topology)
    for layer, pods in groups.items():
        for pod in pods:
            fetch_pod_log(namespace, pod, since_seconds, out_root / f"{pod}.log")

    # Write client wire ops jsonl.
    wire_path = out_root / "client_wire_ops.jsonl"
    with wire_path.open("w") as fh:
        for op in records:
            fh.write(json.dumps(serialise_wire_op(op, monotonic_to_wall)) + "\n")

    # Metadata.
    meta = {
        "scenario": args.scenario,
        "namespace": namespace,
        "mdb_name": mdb_name,
        "mdbs_name": mdbs_name,
        "topology": topology,
        "started_at": capture_started_wall.isoformat(),
        "finished_at": datetime.now(timezone.utc).isoformat(),
        "elapsed_seconds": elapsed,
        "pages_requested": args.pages,
        "pages_read": pages_read,
        "batch_size": args.batch_size,
        "interval_seconds": args.interval_seconds,
        "pods": groups,
        "wire_op_count": len(records),
        "admin_user_env": ADMIN_USER,  # for audit only; not the password
        "verbosity_bumped": verbosity_was_bumped,
    }
    (out_root / "metadata.json").write_text(json.dumps(meta, indent=2))

    # Summary.
    log_files = sorted(out_root.glob("*.log"))
    log_bytes = sum(p.stat().st_size for p in log_files)
    print(f"# fixture {args.scenario}: {len(log_files)} pod logs ({log_bytes/1024:.1f} KB), {len(records)} wire ops")
    print(f"# wrote {out_root}")
    return 0


def parse_args(argv: Optional[list[str]] = None) -> argparse.Namespace:
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument("--scenario", required=True, help="Fixture directory name under testdata/fixtures/.")
    p.add_argument("--namespace", required=True)
    p.add_argument("--mdb-name", required=True)
    p.add_argument("--mdbs-name", default=None, help="Defaults to --mdb-name.")
    p.add_argument("--topology", choices=("rs", "sharded"), required=True)
    p.add_argument("--pages", type=int, default=5)
    p.add_argument("--batch-size", type=int, default=10)
    p.add_argument("--interval-seconds", type=float, default=0.5)
    p.add_argument(
        "--post-settle-seconds",
        type=float,
        default=3.0,
        help="Sleep this long after reads finish before fetching pod logs (lets log buffers flush).",
    )
    p.add_argument(
        "--log-padding-seconds",
        type=int,
        default=15,
        help="Extra seconds added to --since to ensure the log window covers the full scenario.",
    )
    return p.parse_args(argv)


if __name__ == "__main__":
    raise SystemExit(run_capture(parse_args()))
