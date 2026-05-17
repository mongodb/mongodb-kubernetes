"""mongot/mongod debug-log stream analyzer.

Parses the JSON DEBUG logs emitted by mongot (Netty / gRPC) and
mongod (COMMAND / NETWORK / QUERY) and aggregates per-stream events
so we can reason about a single $search cursor's lifecycle across
the mongod → envoy → mongot path.

What this answers
-----------------
- For each gRPC stream on a mongot pod: who opened it, what gRPC
  method was invoked, when it opened/closed, how many HTTP/2 DATA
  frames flowed in/out, total bytes, whether it ended cleanly or
  with RST_STREAM.
- The list of "Prepared N search results" events from
  ``LuceneSearchBatchProducer`` interleaved in time — the producer
  doesn't tag its events with streamId, so we fall back to the
  observation that batch-producer events line up sequentially with
  OUTBOUND DATA frames on the active stream during a paging cursor.
- For mongod (when debug log is enabled): the list of $search
  aggregate / getMore / killCursors commands observed on the wire,
  with their cursorId, namespace, and timestamp.

The cross-side join key is **time** plus the mongot pod hostname.
Per the LB design doc, streamId-on-mongod is an open question with
the mongod team; until that lands, time + cursorId is the best
we have.

Trigger sources for the events parsed here
------------------------------------------
- mongot: needs DEBUG level on ``io.grpc.netty.NettyServerHandler``
  and ``com.xgen.mongot.index.lucene.LuceneSearchBatchProducer``.
  In this dev cluster mongot already runs with broad DEBUG.
- mongod: needs ``db.setLogLevel(2, 'command')`` and
  ``db.setLogLevel(1, 'network')`` to surface aggregate/getMore at
  the wire level. ``set_mongod_debug_logs()`` does this.

Outputs are pure-Python dataclasses so the same module can drive a
report (``print_stream_report``) or be consumed by a test that
asserts on per-stream behaviour.

NOT a pytest test. Used by ``_probe_*`` scripts in tests/search/.
"""

from __future__ import annotations

import json
import re
import subprocess
from collections import defaultdict
from dataclasses import dataclass, field
from datetime import datetime
from typing import Any, Iterable, Optional

# ----------------------------------------------------------------------
# Regex extractors for the parts mongot logs as text rather than JSON.
# ----------------------------------------------------------------------

_STREAM_ID_RE = re.compile(r"streamId=(\d+)")
_LENGTH_RE = re.compile(r"length=(\d+)")
_PATH_RE = re.compile(r":path: (\S+)")
_AUTHORITY_RE = re.compile(r":authority: (\S+)")
_STATUS_RE = re.compile(r":status: (\d+)")
_PEER_RE = re.compile(r"R:/(\S+)")
_LOCAL_RE = re.compile(r"L:/(\S+)")
_BATCH_SIZE_RE = re.compile(r"Prepared (\d+) search results")


# ----------------------------------------------------------------------
# Event + stream data model
# ----------------------------------------------------------------------


@dataclass
class StreamEvent:
    """One HTTP/2 frame or batch-producer event on a mongot stream."""

    timestamp: datetime
    pod: str
    kind: str  # "INBOUND_HEADERS" | "OUTBOUND_HEADERS" | "INBOUND_DATA" | "OUTBOUND_DATA" | "RST_STREAM" | "BATCH"
    length: Optional[int] = None
    extras: dict[str, Any] = field(default_factory=dict)


@dataclass
class StreamSummary:
    """Aggregate view of one HTTP/2 stream on one mongot pod."""

    pod: str
    stream_id: int
    opened_at: Optional[datetime] = None
    closed_at: Optional[datetime] = None
    peer: Optional[str] = None  # envoy side
    grpc_path: Optional[str] = None
    grpc_status: Optional[str] = None
    inbound_data_frames: int = 0
    outbound_data_frames: int = 0
    inbound_bytes: int = 0
    outbound_bytes: int = 0
    rst_stream: bool = False
    events: list[StreamEvent] = field(default_factory=list)

    @property
    def lifetime_seconds(self) -> Optional[float]:
        if self.opened_at and self.closed_at:
            return (self.closed_at - self.opened_at).total_seconds()
        return None


@dataclass
class MongodCommand:
    """One mongod command observed via the COMMAND DEBUG log."""

    timestamp: datetime
    pod: str
    command: str  # "aggregate" | "getMore" | "killCursors" | ...
    namespace: Optional[str] = None
    cursor_id: Optional[int] = None
    duration_ms: Optional[float] = None
    has_search_stage: bool = False
    raw: dict[str, Any] = field(default_factory=dict)


# ----------------------------------------------------------------------
# Log readers
# ----------------------------------------------------------------------


def iter_log_lines(paths_or_pods: Iterable[str], *, namespace: str = "ls-0") -> Iterable[tuple[str, str]]:
    """Yield ``(pod, line)`` for every line in the given log sources.

    Each source is either a file path (read directly) or a pod name
    (read via ``kubectl logs``). The yielded ``pod`` label is the
    file basename or the pod name. ``--since=0s`` is intentionally
    NOT set so the FULL pod log is included.
    """
    for source in paths_or_pods:
        if "/" in source or source.endswith(".log"):
            with open(source) as fh:
                for line in fh:
                    yield (source, line.rstrip("\n"))
        else:
            cmd = ["kubectl", "-n", namespace, "logs", source]
            out = subprocess.check_output(cmd).decode()
            for line in out.splitlines():
                yield (source, line)


def parse_mongot_log_line(pod: str, line: str) -> Optional[StreamEvent | tuple[str, dict]]:
    """Parse one mongot JSON DEBUG line into a StreamEvent or batch tuple.

    Returns:
      - ``StreamEvent`` for HEADERS / DATA / RST_STREAM frames
      - ``("BATCH", {...})`` for ``LuceneSearchBatchProducer`` batches
      - ``None`` for everything else
    """
    line = line.strip()
    if not line or not line.startswith("{"):
        return None
    try:
        rec = json.loads(line)
    except json.JSONDecodeError:
        return None
    msg = rec.get("msg") or ""
    name = rec.get("n") or ""
    t_str = rec.get("t")
    try:
        ts = datetime.fromisoformat(t_str.replace("Z", "+00:00")) if t_str else None
    except (TypeError, ValueError):
        return None

    # --- gRPC/Netty frames ---
    if "NettyServerHandler" in name and ("HEADERS" in msg or "DATA" in msg or "RST_STREAM" in msg):
        sid_m = _STREAM_ID_RE.search(msg)
        if not sid_m:
            return None
        stream_id = int(sid_m.group(1))
        kind = None
        if "INBOUND HEADERS" in msg:
            kind = "INBOUND_HEADERS"
        elif "OUTBOUND HEADERS" in msg:
            kind = "OUTBOUND_HEADERS"
        elif "INBOUND DATA" in msg:
            kind = "INBOUND_DATA"
        elif "OUTBOUND DATA" in msg:
            kind = "OUTBOUND_DATA"
        elif "RST_STREAM" in msg:
            kind = "RST_STREAM"
        else:
            return None

        length_m = _LENGTH_RE.search(msg)
        length = int(length_m.group(1)) if length_m else None
        extras: dict[str, Any] = {"stream_id": stream_id}
        if kind == "INBOUND_HEADERS":
            p = _PATH_RE.search(msg)
            a = _AUTHORITY_RE.search(msg)
            if p:
                extras["grpc_path"] = p.group(1).rstrip(",")
            if a:
                extras["authority"] = a.group(1).rstrip(",")
            peer = _PEER_RE.search(msg)
            local = _LOCAL_RE.search(msg)
            if peer:
                extras["peer"] = peer.group(1)
            if local:
                extras["local"] = local.group(1)
        elif kind == "OUTBOUND_HEADERS":
            s = _STATUS_RE.search(msg)
            if s:
                extras["grpc_status"] = s.group(1)
        return StreamEvent(timestamp=ts, pod=pod, kind=kind, length=length, extras=extras)

    # --- LuceneSearchBatchProducer ---
    if "LuceneSearchBatchProducer" in name:
        m = _BATCH_SIZE_RE.search(msg)
        if m:
            return ("BATCH", {"timestamp": ts, "pod": pod, "size": int(m.group(1))})

    return None


def parse_mongod_log_line(pod: str, line: str) -> Optional[MongodCommand]:
    """Parse one mongod JSON log line into a MongodCommand if it's a relevant command.

    MCK pods wrap each mongod log line in a launcher envelope:
    ``{"logType":"mongodb","contents":"<escaped mongod JSON>"}``. We unwrap
    when the envelope is present, so the same parser handles both raw mongod
    logs and the envelope variant from ``kubectl logs <pod>``.
    """
    line = line.strip()
    if not line or not line.startswith("{"):
        return None
    try:
        rec = json.loads(line)
    except json.JSONDecodeError:
        return None
    # Unwrap MCK agent-launcher envelope if present
    if isinstance(rec.get("logType"), str) and "contents" in rec:
        if rec["logType"] != "mongodb":
            return None
        try:
            rec = json.loads(rec["contents"])
        except (json.JSONDecodeError, TypeError):
            return None
    if rec.get("c") != "COMMAND":
        return None
    attr = rec.get("attr") or {}
    cmd_doc = attr.get("command") or {}
    if not isinstance(cmd_doc, dict):
        return None
    # First key in the command doc is the command name
    cmd_name = None
    for k in cmd_doc:
        if not k.startswith("$") and not k.startswith("_"):
            cmd_name = k
            break
    if cmd_name not in {"aggregate", "getMore", "killCursors"}:
        return None
    ns = attr.get("ns") or attr.get("namespace")
    cursor_id = None
    if cmd_name == "getMore":
        cursor_id = cmd_doc.get("getMore")
    elif cmd_name == "aggregate":
        cursor_doc = cmd_doc.get("cursor") or {}
        cursor_id = cursor_doc.get("id") if isinstance(cursor_doc, dict) else None
    elif cmd_name == "killCursors":
        ids = cmd_doc.get("cursors") or []
        cursor_id = ids[0] if ids else None
    pipeline = cmd_doc.get("pipeline") or []
    has_search = any(isinstance(stage, dict) and "$search" in stage for stage in pipeline)
    t_str = rec.get("t")
    if isinstance(t_str, dict):  # mongod sometimes wraps {"$date": "..."}
        t_str = t_str.get("$date")
    try:
        ts = datetime.fromisoformat(t_str.replace("Z", "+00:00")) if t_str else None
    except (TypeError, ValueError):
        ts = None
    return MongodCommand(
        timestamp=ts,
        pod=pod,
        command=cmd_name,
        namespace=ns,
        cursor_id=cursor_id,
        duration_ms=attr.get("durationMillis"),
        has_search_stage=has_search,
        raw=cmd_doc,
    )


# ----------------------------------------------------------------------
# Aggregation
# ----------------------------------------------------------------------


def build_stream_summaries(
    log_sources: Iterable[str], namespace: str = "ls-0"
) -> tuple[dict[tuple[str, int], StreamSummary], list[dict]]:
    """Read mongot logs and return per-(pod, streamId) summaries plus a list of batch events."""
    summaries: dict[tuple[str, int], StreamSummary] = {}
    batches: list[dict] = []
    for pod, line in iter_log_lines(log_sources, namespace=namespace):
        parsed = parse_mongot_log_line(pod, line)
        if parsed is None:
            continue
        if isinstance(parsed, tuple) and parsed[0] == "BATCH":
            batches.append(parsed[1])
            continue
        ev: StreamEvent = parsed  # type: ignore[assignment]
        stream_id = ev.extras["stream_id"]
        key = (pod, stream_id)
        summary = summaries.setdefault(key, StreamSummary(pod=pod, stream_id=stream_id))
        summary.events.append(ev)
        if ev.kind == "INBOUND_HEADERS":
            summary.opened_at = ev.timestamp
            summary.peer = ev.extras.get("peer") or summary.peer
            summary.grpc_path = ev.extras.get("grpc_path") or summary.grpc_path
        elif ev.kind == "OUTBOUND_HEADERS":
            summary.grpc_status = ev.extras.get("grpc_status") or summary.grpc_status
        elif ev.kind == "INBOUND_DATA":
            summary.inbound_data_frames += 1
            summary.inbound_bytes += ev.length or 0
        elif ev.kind == "OUTBOUND_DATA":
            summary.outbound_data_frames += 1
            summary.outbound_bytes += ev.length or 0
            summary.closed_at = ev.timestamp
        elif ev.kind == "RST_STREAM":
            summary.rst_stream = True
            summary.closed_at = ev.timestamp
    return summaries, batches


def read_mongod_commands(log_sources: Iterable[str], namespace: str = "ls-0") -> list[MongodCommand]:
    out: list[MongodCommand] = []
    for pod, line in iter_log_lines(log_sources, namespace=namespace):
        cmd = parse_mongod_log_line(pod, line)
        if cmd is not None:
            out.append(cmd)
    return out


# ----------------------------------------------------------------------
# Mongod log-level helper
# ----------------------------------------------------------------------


def set_mongod_debug_logs(mongo_client, *, command_level: int = 2, network_level: int = 2) -> dict:
    """Bump mongod global log level for COMMAND/NETWORK and return the previous setting.

    Reverse with ``set_mongod_debug_logs(client, command_level=0, network_level=0)``.
    Required for ``$search`` aggregate / getMore wire-level visibility, which sits
    behind verbosity 2 on the ``command`` component.

    The default ``network_level=2`` is what surfaces mongod's gRPC egress
    session lifecycle — ``LOGV2_DEBUG(7401401)`` "Constructed a new gRPC
    egress session" and ``7401403`` "Finished cleaning up a gRPC egress
    session" — which carries the ``clientId`` UUID that envoy and mongot
    log on the same stream. Without it the cross-side join (mongod ↔ envoy
    ↔ mongot) collapses to time-based correlation, which is fragile under
    load. See ``tmp/search-caching-investigation/observability-followup.md``
    for the full rationale.
    """
    res = mongo_client.admin.command(
        "setParameter",
        1,
        logComponentVerbosity={
            "command": {"verbosity": command_level},
            "network": {"verbosity": network_level},
            "query": {"verbosity": 1},
        },
    )
    return res


# ----------------------------------------------------------------------
# Pretty printers
# ----------------------------------------------------------------------


def print_stream_report(
    summaries: dict[tuple[str, int], StreamSummary],
    batches: list[dict],
) -> None:
    print(f"\n=== mongot HTTP/2 stream report — {len(summaries)} stream(s) across {len({k[0] for k in summaries})} pod(s) ===")
    by_pod: dict[str, list[StreamSummary]] = defaultdict(list)
    for (pod, _sid), s in summaries.items():
        by_pod[pod].append(s)
    for pod, streams in by_pod.items():
        print(f"\n  pod={pod}")
        streams.sort(key=lambda s: s.opened_at or datetime.max)
        for s in streams:
            lifetime = f"{s.lifetime_seconds:.2f}s" if s.lifetime_seconds is not None else "n/a"
            print(
                f"    stream={s.stream_id:<3}  peer={s.peer or '?':<22}  path={s.grpc_path or '?':<55}  "
                f"status={s.grpc_status or '?':<3}  in={s.inbound_data_frames}f/{s.inbound_bytes}B  "
                f"out={s.outbound_data_frames}f/{s.outbound_bytes}B  rst={s.rst_stream}  lifetime={lifetime}"
            )

    if batches:
        print(f"\n=== LuceneSearchBatchProducer events — {len(batches)} total ===")
        by_pod_b: dict[str, list[dict]] = defaultdict(list)
        for b in batches:
            by_pod_b[b["pod"]].append(b)
        for pod, bs in by_pod_b.items():
            bs.sort(key=lambda b: b["timestamp"] or datetime.max)
            sizes = ", ".join(str(b["size"]) for b in bs)
            total = sum(b["size"] for b in bs)
            print(f"  pod={pod}  batches={len(bs)}  sizes=[{sizes}]  total={total}")


def print_mongod_command_report(cmds: list[MongodCommand]) -> None:
    print(f"\n=== mongod commands — {len(cmds)} total ===")
    by_cmd: dict[str, int] = defaultdict(int)
    for c in cmds:
        by_cmd[c.command] += 1
    print("  totals:", dict(by_cmd))
    search_cursors: dict[Optional[int], list[MongodCommand]] = defaultdict(list)
    for c in cmds:
        if c.has_search_stage or c.command in {"getMore", "killCursors"}:
            search_cursors[c.cursor_id].append(c)
    for cid, cs in search_cursors.items():
        cs.sort(key=lambda c: c.timestamp or datetime.max)
        kinds = [c.command for c in cs]
        first = cs[0].timestamp.isoformat() if cs[0].timestamp else "?"
        last = cs[-1].timestamp.isoformat() if cs[-1].timestamp else "?"
        print(
            f"  cursor_id={cid}  pod={cs[0].pod}  events={len(cs)}  "
            f"kinds={kinds[:8]}{'...' if len(kinds) > 8 else ''}  span={first} → {last}"
        )


__all__ = [
    "StreamEvent",
    "StreamSummary",
    "MongodCommand",
    "iter_log_lines",
    "parse_mongot_log_line",
    "parse_mongod_log_line",
    "build_stream_summaries",
    "read_mongod_commands",
    "set_mongod_debug_logs",
    "print_stream_report",
    "print_mongod_command_report",
]
