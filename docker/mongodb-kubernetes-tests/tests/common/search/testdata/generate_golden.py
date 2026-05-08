"""Generate golden analyzer output for a captured fixture.

Reads ``testdata/fixtures/<scenario>/`` (pod logs + ``client_wire_ops.jsonl``
+ ``metadata.json``), runs the current analyzer's render pipeline, and
writes ``golden/`` with:

- ``cursor_trees.txt``           per-cursor tree (RS) or sharded-cursor tree
- ``unified_timeline.txt``       full unified timeline (truncated to --timeline-max)
- ``lsid_<hex>.txt``             per-detected-lsid filtered timeline (no color)
- ``detected_lsids.txt``         summary printed at top of CLI default output

Output is deterministic for diff-based regression checks. Color is
forced OFF; ``--timeline-max=1000`` so terminal-width quirks don't leak in.

Usage (from devc):
  python -m tests.common.search.testdata.generate_golden \\
      --scenario rs-paging-clean

To regenerate for every fixture under testdata/fixtures/:
  for d in docker/mongodb-kubernetes-tests/tests/common/search/testdata/fixtures/*/; do
      python -m tests.common.search.testdata.generate_golden --scenario "$(basename $d)"
  done
"""

from __future__ import annotations

import argparse
import contextlib
import io
import json
import os
import pathlib
import sys
from datetime import datetime

from tests.common.search.log_analyzer.analyzer import (
    ClientWireOp,
    build_stream_summaries,
    collect_lsids_in_window,
    merge_envoy_access_log_into_streams,
    parse_client_wire_ops,
    parse_envoy_debug_log,
    print_cursor_trees,
    print_lsid_timeline,
    print_lsids_summary,
    print_sharded_cursor_trees,
    print_unified_timeline,
    read_envoy_access_log,
    read_mongod_commands,
    read_mongod_sessions,
    read_mongos_commands,
    read_mongos_remote_requests,
    read_mongot_interceptor_events,
)
from tests.common.search.log_analyzer.store import (
    LogStore,
    build_cursor_trees_sql,
    build_sharded_cursor_trees_sql,
    build_unified_timeline_sql,
)

FIXTURES = pathlib.Path(__file__).resolve().parent / "fixtures"


def _fixtures_dir() -> pathlib.Path:
    """Allow the regression test to point at a shadow fixtures tree.

    The env override is used by ``test_analyzer_golden.py`` to avoid
    clobbering the committed golden files. Default stays the in-tree
    fixtures dir so the CLI usage in the module docstring still works.
    """
    override = os.environ.get("MCK_ANALYZER_FIXTURES_DIR")
    return pathlib.Path(override) if override else FIXTURES


def _capture_stdout(fn, *args, **kwargs) -> str:
    """Capture stdout written by ``fn(*args, **kwargs)``; return as str."""
    buf = io.StringIO()
    with contextlib.redirect_stdout(buf):
        fn(*args, **kwargs)
    return buf.getvalue()


def _load_wire_ops(jsonl_path: pathlib.Path) -> list[ClientWireOp]:
    """Read captured wire ops back into analyzer ClientWireOp dataclasses.

    The capture writes ISO timestamps; rehydrate into datetime so the
    analyzer's join paths (which expect ``datetime``) work unchanged.
    """
    records: list[ClientWireOp] = []
    if not jsonl_path.exists():
        return records
    for line in jsonl_path.read_text().splitlines():
        if not line.strip():
            continue
        d = json.loads(line)
        ts_raw = d.get("timestamp")
        if not isinstance(ts_raw, str):
            # ClientWireOp requires a wall-clock timestamp; skip records that
            # lack one (older fixture format / partial captures).
            continue
        ts = datetime.fromisoformat(ts_raw)
        records.append(
            ClientWireOp(
                phase=d["phase"],
                command_name=d["command_name"],
                request_id=d["request_id"],
                timestamp=ts,
                server_connection_id=d.get("server_connection_id"),
                lsid=d.get("lsid"),
                cursor_id=d.get("cursor_id"),
                duration_micros=d.get("duration_micros"),
                n_returned=d.get("n_returned"),
                database_name=d.get("database_name"),
                operation_id=d.get("operation_id"),
                failure=d.get("failure"),
            )
        )
    return records


def _group_logs(fixture_dir: pathlib.Path, meta: dict) -> dict[str, list[str]]:
    """Return {layer: [log path...]} from metadata's pods dict."""
    out: dict[str, list[str]] = {}
    for layer, pods in meta.get("pods", {}).items():
        paths = []
        for pod in pods:
            p = fixture_dir / f"{pod}.log"
            if p.is_file():
                paths.append(str(p))
        out[layer] = paths
    return out


def _generate(scenario: str, timeline_max: int) -> int:
    fixture = _fixtures_dir() / scenario
    if not fixture.is_dir():
        raise SystemExit(f"no such fixture dir: {fixture}")
    meta = json.loads((fixture / "metadata.json").read_text())
    namespace = meta["namespace"]
    topology = meta["topology"]
    mdb_name = meta["mdb_name"]
    mdbs_name = meta["mdbs_name"]
    paths = _group_logs(fixture, meta)

    print(f"# scenario={scenario} topology={topology} ns={namespace}", file=sys.stderr)

    # Parse all layers.
    mongos_paths = paths.get("mongos", [])
    mongod_paths = paths.get("mongod", [])
    mongot_paths = paths.get("mongot", [])
    envoy_paths = paths.get("envoy", [])

    mongos_cmds = read_mongos_commands(mongos_paths, namespace=namespace) if mongos_paths else []
    mongos_reqs = read_mongos_remote_requests(mongos_paths, namespace=namespace) if mongos_paths else []
    mongod_cmds = read_mongod_commands(mongod_paths, namespace=namespace) if mongod_paths else []
    mongod_sessions = read_mongod_sessions(mongod_paths, namespace=namespace) if mongod_paths else []
    envoy_streams = parse_envoy_debug_log(envoy_paths, namespace=namespace) if envoy_paths else []
    envoy_access = read_envoy_access_log(envoy_paths, namespace=namespace) if envoy_paths else []
    envoy_streams = merge_envoy_access_log_into_streams(envoy_streams, envoy_access)
    mongot_streams, mongot_batches = (
        build_stream_summaries(mongot_paths, namespace=namespace) if mongot_paths else ({}, [])
    )
    mongot_opens, mongot_cmds_log = (
        read_mongot_interceptor_events(mongot_paths, namespace=namespace) if mongot_paths else ([], [])
    )

    wire_ops_raw = _load_wire_ops(fixture / "client_wire_ops.jsonl")
    client_ops = parse_client_wire_ops(wire_ops_raw)

    golden = fixture / "golden"
    golden.mkdir(exist_ok=True)

    # Build per-topology tree + timeline.
    if topology == "sharded":
        import re

        shard_re = re.compile(rf"^{re.escape(mdb_name)}-(\d+)-\d+$")
        shard_indices: set[int] = set()
        for p in mongod_paths:
            m = shard_re.match(os.path.basename(p)[:-4])
            if m is not None:
                shard_indices.add(int(m.group(1)))
        shard_pod_prefixes = {f"{mdb_name}-{i}": f"{mdb_name}-{i}-" for i in sorted(shard_indices)}
        shard_mongot_pod_prefixes = {
            f"{mdb_name}-{i}": f"{mdbs_name}-search-0-{mdb_name}-{i}-" for i in sorted(shard_indices)
        }
        store = LogStore()
        store.load_from_parsed_records(
            client_ops=client_ops,
            mongod_commands=mongod_cmds,
            mongod_sessions=mongod_sessions,
            mongos_commands=mongos_cmds,
            mongos_remote_requests=mongos_reqs,
            envoy_streams=envoy_streams,
            envoy_access=envoy_access,
            mongot_streams=mongot_streams,
            mongot_batches=mongot_batches,
            mongot_stream_opens=mongot_opens,
            mongot_cmds=mongot_cmds_log,
        )
        trees = build_sharded_cursor_trees_sql(
            store,
            shard_pod_prefixes=shard_pod_prefixes,
            shard_mongot_pod_prefixes=shard_mongot_pod_prefixes,
        )
        timeline = build_unified_timeline_sql(store)
        (golden / "sharded_cursor_trees.txt").write_text(_capture_stdout(print_sharded_cursor_trees, trees))
        # Keep a placeholder file for consistency with RS naming so test code
        # can check both paths exist.
        (golden / "cursor_trees.txt").write_text("# sharded topology — see sharded_cursor_trees.txt\n")
    else:
        store = LogStore()
        store.load_from_parsed_records(
            client_ops=client_ops,
            mongod_commands=mongod_cmds,
            mongod_sessions=mongod_sessions,
            envoy_streams=envoy_streams,
            envoy_access=envoy_access,
            mongot_streams=mongot_streams,
            mongot_batches=mongot_batches,
            mongot_stream_opens=mongot_opens,
            mongot_cmds=mongot_cmds_log,
        )
        trees = build_cursor_trees_sql(store)
        timeline = build_unified_timeline_sql(store)
        (golden / "cursor_trees.txt").write_text(_capture_stdout(print_cursor_trees, trees))

    # Unified timeline + lsid summary.
    (golden / "unified_timeline.txt").write_text(
        _capture_stdout(print_unified_timeline, timeline, max_events=timeline_max)
    )
    (golden / "detected_lsids.txt").write_text(_capture_stdout(print_lsids_summary, timeline))

    # Per-lsid filtered timeline — color forced OFF.
    seen_lsids = sorted(collect_lsids_in_window(timeline).keys())
    for lsid in seen_lsids:
        out_path = golden / f"lsid_{lsid}.txt"
        out_path.write_text(_capture_stdout(print_lsid_timeline, timeline, lsid, color=False))

    # Row-count manifest so the SQLite migration can use this as a target.
    manifest = {
        "scenario": scenario,
        "topology": topology,
        "rows": {
            "mongod_commands": len(mongod_cmds),
            "mongod_sessions": len(mongod_sessions),
            "mongos_commands": len(mongos_cmds),
            "mongos_remote_requests": len(mongos_reqs),
            "envoy_streams": len(envoy_streams),
            "envoy_access": len(envoy_access),
            "mongot_streams": len(mongot_streams),
            "mongot_batches": len(mongot_batches),
            "mongot_stream_opens": len(mongot_opens),
            "mongot_cmds": len(mongot_cmds_log),
            "client_wire_ops": len(client_ops),
        },
        "lsids": seen_lsids,
        "timeline_events": len(timeline),
        "cursor_trees": len(trees),
    }
    (golden / "row_counts.json").write_text(json.dumps(manifest, indent=2))

    print(
        f"# wrote golden for {scenario}: {len(trees)} trees, "
        f"{len(timeline)} timeline events, {len(seen_lsids)} lsid(s)"
    )
    return 0


def parse_args(argv=None) -> argparse.Namespace:
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument("--scenario", required=True, help="Fixture directory name.")
    p.add_argument("--timeline-max", type=int, default=1000)
    return p.parse_args(argv)


if __name__ == "__main__":
    raise SystemExit(_generate(parse_args().scenario, parse_args().timeline_max))
