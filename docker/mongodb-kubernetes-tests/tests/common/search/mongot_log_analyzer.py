"""mongot/mongod/envoy/pymongo debug-log analyzer.

Parses the JSON DEBUG logs emitted by mongot (Netty / gRPC) and
mongod (COMMAND / NETWORK / QUERY), the envoy proxy's runtime debug
logs (http2 / http frame-level), and the in-process events captured
by ``SearchConnectivityTool``'s pymongo ``CommandListener``, then
aggregates them into a single chronologically-ordered timeline so we
can reason about a $search cursor's lifecycle across all four sides.

What this answers
-----------------
- For each gRPC stream on a mongot pod: who opened it, what gRPC
  method was invoked, when it opened/closed, how many HTTP/2 DATA
  frames flowed in/out, total bytes, whether it ended cleanly or
  with RST_STREAM.
- For each mongod node: the list of $search aggregate / getMore /
  killCursors commands observed on the wire (COMMAND verbosity 2),
  plus the gRPC egress-session lifecycle on the upstream side
  (NETWORK verbosity 2 — ``LOGV2_DEBUG(7401401)`` / ``7401403``).
  Each ``MongodSession`` carries the ``clientId`` UUID that envoy
  logs as the ``mongodb-clientid`` request header.
- For each envoy LB pod with ``http2:debug`` enabled: per-DATA-frame
  events plus the per-request ``mongodb-clientid`` extracted from
  the request headers — the join key for envoy ↔ mongod and
  (post-mongot-patch) envoy ↔ mongot.
- For the pymongo client side: every wire-protocol round-trip the
  CommandListener captured (``ClientWireOp``), keyed on ``lsid``
  and ``server_connection_id`` — both of which mongod echoes in
  the COMMAND ``Slow query`` records, giving us an exact-match
  join from the test process to mongod log lines.

The cross-side join graph
-------------------------
With ``network:2`` on mongod and envoy access logs enabled, every
cursor's lifecycle is identifiable by three keys, no time tolerance:

  pymongo (lsid + server_connection_id)
     ↔ mongod COMMAND records  (attr.command.lsid + ctx=conn<N>)
     ↔ mongod NETWORK record   (clientId UUID, id=7401401)
     ↔ envoy access log / debug log  (mongodb-clientid request header)
     ↔ mongot (TBD — needs interceptor log patch for clientId
       at stream open; cursorId addKeyValue on the command TRACE)

Until the mongot patch lands, envoy ↔ mongot and mongod ↔ mongot
still rely on time + pod hostname.

Trigger sources for the events parsed here
------------------------------------------
- mongot: needs DEBUG level on ``io.grpc.netty.NettyServerHandler``
  and ``com.xgen.mongot.index.lucene.LuceneSearchBatchProducer``.
  In this dev cluster mongot already runs with broad DEBUG.
- mongod: ``set_mongod_debug_logs()`` bumps COMMAND verbosity to 2
  (for aggregate/getMore/killCursors records) and NETWORK verbosity
  to 2 (for the gRPC egress session lifecycle that carries the
  cross-side clientId UUID).
- envoy: POST to ``/logging?paths=http2:debug,http:debug,router:debug``
  on the admin endpoint to surface per-DATA-frame visibility and
  ``mongodb-clientid`` request headers. Capture
  ``kubectl logs <envoy-pod>`` output for the lifetime of the test
  and POST ``level=info`` to restore. (Wrapped by
  ``_probe_envoy_debug_h2.py`` for ad-hoc runs.)
- pymongo: ``SearchConnectivityTool`` installs a CommandListener at
  construction time; pass ``tool.listener._records`` or use a
  per-call snapshot via ``tool._begin_capture`` / ``_end_capture``.

Outputs are pure-Python dataclasses so the same module can drive a
report (``print_stream_report``, ``print_unified_timeline``) or be
consumed by a test that asserts on per-stream behaviour.

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

# Mongod NETWORK records (verbosity 2):
#   id=7401401  "Constructed a new gRPC egress session"
#     attr.session = {id, clientId, remote}
#   id=7401403  "Finished cleaning up a gRPC egress session"
#     attr.session = {id, clientId, remote}; attr.status = "..."
_MONGOD_LOG_ID_SESSION_OPEN = 7401401
_MONGOD_LOG_ID_SESSION_CLOSE = 7401403

# Mongod ctx field "conn<N>" — the server_connection_id pymongo's
# CommandListener surfaces.
_CONN_CTX_RE = re.compile(r"^conn(\d+)$")

# Envoy debug-log per-stream signals. Examples:
#   "[ConnectionId:3070] new stream"
#   "[ConnectionId:3070,StreamId:15315301757811320433] request headers complete (end_stream=false):"
#   "  'mongodb-clientid', '88caab85-2152-4342-a786-77167ddd3dda'"
#   "  ':path', '/mongodb.CommandService/UnauthenticatedCommandStream'"
#   "[ConnectionId:3070] Http2Visitor: remaining data payload: 517, stream_id: 5, end_stream: false"
#   "[ConnectionId:3070] stream 5 closed: 0"
#   "  'grpc-status', '0'"
# Envoy 1.27+ wraps the connection/stream id in a ``Tags`` envelope:
#   "[Tags: \"ConnectionId\":\"1225\",\"StreamId\":\"3770777754489420355\"]"
# while older versions print the bare form:
#   "[ConnectionId:3070,StreamId:15315301757811320433]"
# This regex accepts both. The capture groups are (connection_id,
# stream_id?).
_ENVOY_CONN_RE = re.compile(
    r"\[(?:Tags:\s*)?\"?ConnectionId\"?[:=]\"?(\d+)\"?"
    r"(?:,\s*\"?StreamId\"?[:=]\"?(\d+)\"?)?\]"
)
_ENVOY_HTTP2_DATA_RE = re.compile(
    r"Http2Visitor: remaining data payload: (\d+), stream_id: (\d+), end_stream: (true|false)"
)
_ENVOY_STREAM_CLOSE_RE = re.compile(r"stream (\d+) closed: (\d+)")
_ENVOY_HEADER_LINE_RE = re.compile(r"^\s*'([^']+)',\s*'([^']*)'\s*$")
_ENVOY_TS_RE = re.compile(r"\[(\d{4}-\d{2}-\d{2})?[ T]?(\d{2}:\d{2}:\d{2}\.\d{3})\]")


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
    # Cross-side join keys lifted from the COMMAND record (verbosity 2):
    #   lsid: hex form of the binary UUID inside attr.command.lsid.id
    #         — the exact value pymongo's CommandStartedEvent.command["lsid"]
    #         carries on the client side.
    #   server_connection_id: pymongo's server_connection_id (the
    #         "conn<N>" suffix in mongod's ``ctx``). We parse the ``ctx``
    #         field of the log record because pymongo identifies the
    #         connection by integer; mongod prints it as ``conn<N>``.
    lsid: Optional[str] = None
    server_connection_id: Optional[int] = None
    raw: dict[str, Any] = field(default_factory=dict)


@dataclass
class MongodSession:
    """One gRPC egress session on a mongod node, from network:2 records.

    Constructed from the pair of ``LOGV2_DEBUG`` records:
    ``id=7401401`` (construction) and ``id=7401403`` (cleanup), both
    living in mongod's NETWORK component. The session id is unique
    per mongod process; the ``client_id`` UUID is the cross-side join
    key — envoy logs it as the ``mongodb-clientid`` request header,
    and mongot reads it from the same header (and, after the proposed
    mongot patch, will log it at DEBUG on every new stream).

    ``cursor_id`` is filled in by ``correlate_sessions_with_cursors``;
    on a single mongod pod, a session's cursor is the first cursor
    registered (``id=8928407``) inside its open/close window.
    """

    pod: str
    session_id: int
    client_id: str
    remote: Optional[str]
    opened_at: datetime
    closed_at: Optional[datetime] = None
    status: Optional[str] = None  # gRPC final status (from 7401403)
    cursor_id: Optional[int] = None


@dataclass
class EnvoyStream:
    """One HTTP/2 stream observed on an envoy LB pod with http2:debug enabled.

    Each entry combines the per-stream signals envoy emits at runtime debug:
    the HCM 64-bit ``StreamId`` from the connection-manager (printed at
    request-headers-complete), the wire HTTP/2 ``stream_id`` (printed by
    the codec on every DATA frame), the request path, the
    ``mongodb-clientid`` header value, and the eventual stream-close
    grpc-status / RST_STREAM signal.

    The ``client_id`` is the load-bearing cross-side join key — it
    matches exactly the value mongod's ``LOGV2_DEBUG(7401401)`` writes
    as ``attr.session.clientId``.
    """

    pod: str
    connection_id: int
    stream_id: int  # HTTP/2 wire stream id (the codec's "stream X closed: Y")
    hcm_stream_id: Optional[int] = None  # the HCM-side 64-bit StreamId
    path: Optional[str] = None  # ":path" header
    client_id: Optional[str] = None  # 'mongodb-clientid' header
    opened_at: Optional[datetime] = None
    closed_at: Optional[datetime] = None
    grpc_status: Optional[str] = None
    rst_stream: bool = False
    inbound_data_frames: int = 0
    outbound_data_frames: int = 0
    inbound_bytes: int = 0
    outbound_bytes: int = 0

    @property
    def lifetime_seconds(self) -> Optional[float]:
        if self.opened_at and self.closed_at:
            return (self.closed_at - self.opened_at).total_seconds()
        return None


@dataclass
class ClientWireOp:
    """One wire-protocol command captured by the pymongo CommandListener.

    Mirror of ``connectivity.ClientWireOp`` (kept here for the analyzer's
    own parser surface, so callers don't have to import the connectivity
    module). Constructed from either the connectivity tool's listener
    records (in-process objects) OR from a separate replay format if the
    listener output was serialised between runs.

    The ``lsid`` and ``server_connection_id`` fields are the join keys
    to ``MongodCommand``; ``request_id`` is the wire-protocol request id
    pymongo allocated (NOT echoed by mongod at any verbosity today).
    """

    phase: str  # "started" | "succeeded" | "failed"
    command_name: str
    request_id: int
    timestamp: datetime  # converted from time.monotonic() to wall clock if known
    server_connection_id: Optional[int] = None
    lsid: Optional[str] = None
    cursor_id: Optional[int] = None
    duration_micros: Optional[int] = None
    n_returned: Optional[int] = None
    database_name: Optional[str] = None
    operation_id: Optional[int] = None
    failure: Optional[str] = None


@dataclass
class CursorTreeWireOp:
    """One client wire op (aggregate / getMore / killCursors) plus the
    matching lower-layer events that op drove on this run.

    A wire op is identified by its pymongo ``request_id``; the
    ``started`` and ``succeeded`` (or ``failed``) records share that id.
    We collapse the pair into a single record here: ``client_started``
    is the wire-op start time, ``client_succeeded`` is the end time;
    ``duration_micros``/``n_returned`` come from the succeeded side.

    The lower-layer fields are filled in by ``build_cursor_trees`` via
    deterministic join keys when those keys are available, falling back
    to time-window matching otherwise (see the join rules in
    ``build_cursor_trees``). Fields that don't apply to a given wire-op
    type stay None (e.g. ``envoy_stream`` is only set on aggregate,
    because the gRPC stream to mongot opens once per cursor).
    """

    command_name: str  # "aggregate" | "getMore" | "killCursors"
    request_id: int
    client_started: Optional[datetime] = None
    client_succeeded: Optional[datetime] = None
    duration_micros: Optional[int] = None
    n_returned: Optional[int] = None
    server_connection_id: Optional[int] = None
    lsid: Optional[str] = None
    cursor_id: Optional[int] = None
    failure: Optional[str] = None

    # Lower layers
    mongod_cmd: Optional["MongodCommand"] = None
    mongod_session_open: Optional["MongodSession"] = None
    mongod_session_close: Optional["MongodSession"] = None
    envoy_stream: Optional["EnvoyStream"] = None
    mongot_stream_open: Optional[dict] = None  # patched mongot interceptor record
    mongot_stream_close: Optional["StreamSummary"] = None
    mongot_batches: list[dict] = field(default_factory=list)
    mongot_cmd: Optional[dict] = None  # patched mongot command record

    # The "(cached)" analytic predicate: True iff at least one mongot
    # ``batch_prepared`` event landed inside this op's wire window
    # (with a small slack — see ``_MONGOT_BATCH_MATCH_SLACK_SECONDS``).
    served_fresh_from_mongot: bool = False


@dataclass
class CursorTree:
    """All events for one ``$search`` cursor, grouped under one root.

    A tree is identified by the mongod-side ``cursor_id`` (the id mongod
    allocated in the aggregate's response). All wire ops that quote that
    cursor id — the originating aggregate, every paging getMore, the
    final killCursors — hang off the same tree.

    Join keys captured on the tree itself (vs each wire op):

    - ``client_lsid``  — the pymongo logical session id; identifies the
      client side across every op on this cursor.
    - ``mongod_pod`` / ``mongot_pod`` — load-balanced placement.
    - ``client_id_uuid`` — the cross-layer UUID that appears in mongod's
      NETWORK record, envoy's request headers, and (post-patch) mongot's
      interceptor log. Load-bearing because it's the only join key that
      crosses envoy↔mongot today.
    - ``mongot_stream_id`` — the Netty stream id mongot logged for this
      cursor's gRPC stream.
    """

    cursor_id: int
    client_lsid: Optional[str] = None
    mongod_pod: Optional[str] = None
    mongot_pod: Optional[str] = None
    client_id_uuid: Optional[str] = None
    mongot_stream_id: Optional[int] = None
    wire_ops: list[CursorTreeWireOp] = field(default_factory=list)


# Slack window when matching a mongot batch_prepared event to a client
# wire op. Mongot, envoy, mongod and the test process all run with their
# own NTP'd clocks; ±50ms is comfortably more than the observed skew in
# the live ls-0 cluster and still tight enough to distinguish two
# adjacent getMores (which are 100ms+ apart at the default 0.05s
# interval used by ``paging_search``).
_MONGOT_BATCH_MATCH_SLACK_SECONDS = 0.050


@dataclass
class TimelineEvent:
    """One event on the unified cross-layer timeline.

    ``layer`` is one of:
      - ``"client"``       — pymongo CommandListener record
      - ``"mongod.cmd"``   — mongod COMMAND record (aggregate/getMore/...)
      - ``"mongod.net"``   — mongod NETWORK record (egress session)
      - ``"envoy"``        — envoy debug-log frame / per-stream signal
      - ``"mongot.frame"`` — mongot Netty HEADERS/DATA frame
      - ``"mongot.batch"`` — mongot LuceneSearchBatchProducer record
    """

    timestamp: datetime
    layer: str
    pod: Optional[str]
    kind: str  # layer-specific event kind
    # Cross-side join keys lifted onto the event for filtering. None when
    # the layer doesn't surface that key yet (see followup.md gap table).
    client_id: Optional[str] = None  # UUID — mongod NETWORK ⇔ envoy ⇔ mongot
    lsid: Optional[str] = None  # client ⇔ mongod
    cursor_id: Optional[int] = None
    server_connection_id: Optional[int] = None
    stream_id: Optional[int] = None  # envoy / mongot HTTP/2 stream id
    session_id: Optional[int] = None  # mongod NETWORK session id
    details: dict[str, Any] = field(default_factory=dict)


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
            # cursorId / clientId are not in this log site today, but we
            # surface them if a future mongot patch adds them via SLF4J
            # key-value pairs (the analyzer is backwards-compatible).
            return (
                "BATCH",
                {
                    "timestamp": ts,
                    "pod": pod,
                    "size": int(m.group(1)),
                    "cursor_id": _maybe_int(rec.get("cursorId")),
                    "client_id": _maybe_str(rec.get("clientId")),
                },
            )

    # --- MongoDbGrpcProtocolInterceptor (proposed mongot patch) ---
    # Pre-patch: this log site does not emit at DEBUG.
    # Post-patch: one DEBUG line per new gRPC stream with the
    # ``clientId`` UUID + grpc method name. The analyzer remains
    # backwards-compatible: when this record is absent, the envoy↔mongot
    # join just stays at time-based correlation.
    if "MongoDbGrpcProtocolInterceptor" in name and "New mongot gRPC stream" in msg:
        # SLF4J/Logback key-value pairs land under ``attr`` in mongot's JSON
        # encoder (verified against the patched build).
        attr = rec.get("attr") or {}
        return (
            "MONGOT_STREAM_OPEN",
            {
                "timestamp": ts,
                "pod": pod,
                "client_id": _maybe_str(attr.get("clientId")),
                "path": _maybe_str(attr.get("path")),
            },
        )

    # --- Mongot command TRACE logs (also a proposed patch) ---
    # SearchCommand / GetMoreCommand / KillCursorsCommand each add a
    # ``cursorId`` key-value to their existing TRACE entry. If absent
    # the analyzer skips this branch.
    if name.endswith((".SearchCommand", ".GetMoreCommand", ".KillCursorsCommand")):
        attr = rec.get("attr") or {}
        cid = attr.get("cursorId") or attr.get("cursorIds")
        if cid is not None:
            return (
                "MONGOT_CMD",
                {
                    "timestamp": ts,
                    "pod": pod,
                    "command": name.rsplit(".", 1)[-1],
                    "cursor_id": _maybe_int(cid if not isinstance(cid, list) else (cid[0] if cid else None)),
                    "client_id": _maybe_str(attr.get("clientId")),
                },
            )

    return None


def _maybe_int(value: Any) -> Optional[int]:
    if value is None:
        return None
    try:
        return int(value)
    except (TypeError, ValueError):
        return None


def _maybe_str(value: Any) -> Optional[str]:
    if value is None:
        return None
    return str(value)


def _unwrap_mongod_record(line: str) -> Optional[dict]:
    """Parse one mongod log line and unwrap the MCK launcher envelope.

    Returns the bare mongod record (a dict with keys c/t/id/msg/attr/ctx)
    on success, or None for blank / non-JSON / non-mongod lines. Shared
    between the COMMAND and NETWORK parsers below.
    """
    line = line.strip()
    if not line or not line.startswith("{"):
        return None
    try:
        rec = json.loads(line)
    except json.JSONDecodeError:
        return None
    if isinstance(rec.get("logType"), str) and "contents" in rec:
        if rec["logType"] != "mongodb":
            return None
        try:
            rec = json.loads(rec["contents"])
        except (json.JSONDecodeError, TypeError):
            return None
    return rec


def _mongod_record_timestamp(rec: dict) -> Optional[datetime]:
    t_str = rec.get("t")
    if isinstance(t_str, dict):
        t_str = t_str.get("$date")
    try:
        return datetime.fromisoformat(t_str.replace("Z", "+00:00")) if t_str else None
    except (TypeError, ValueError):
        return None


def _extract_lsid_hex(cmd_doc: Any) -> Optional[str]:
    """Return the lsid as hex string from a mongod COMMAND record's command doc.

    mongod prints lsid as ``{"id": {"$uuid": "8d8a...-..."}}`` (extended-json)
    or as ``{"id": {"$binary": {"base64": "...", "subType": "04"}}}``. Both
    surfaces map to the same UUID bytes; pymongo's CommandStartedEvent
    surfaces the same bytes as ``Binary(b'...', 4)`` which we serialise as
    its ``.hex()``. Return the hex form WITHOUT dashes so the join key is
    a single canonical representation.
    """
    if not isinstance(cmd_doc, dict):
        return None
    lsid = cmd_doc.get("lsid")
    if not isinstance(lsid, dict):
        return None
    inner = lsid.get("id")
    if isinstance(inner, dict):
        if "$uuid" in inner:
            return str(inner["$uuid"]).replace("-", "").lower()
        b = inner.get("$binary")
        if isinstance(b, dict) and "base64" in b:
            try:
                import base64

                return base64.b64decode(b["base64"]).hex()
            except Exception:  # pragma: no cover — defensive
                return None
    if isinstance(inner, str):
        return inner.replace("-", "").lower()
    return None


def _extract_server_connection_id(ctx: Any) -> Optional[int]:
    """Extract the integer connection id from a mongod ``ctx: "conn<N>"``."""
    if not isinstance(ctx, str):
        return None
    m = _CONN_CTX_RE.match(ctx)
    return int(m.group(1)) if m else None


def parse_mongod_log_line(pod: str, line: str) -> Optional[MongodCommand]:
    """Parse one mongod JSON log line into a MongodCommand if it's a relevant command.

    MCK pods wrap each mongod log line in a launcher envelope:
    ``{"logType":"mongodb","contents":"<escaped mongod JSON>"}``. We unwrap
    when the envelope is present, so the same parser handles both raw mongod
    logs and the envelope variant from ``kubectl logs <pod>``.

    Returns a ``MongodCommand`` with the cross-side join keys (``lsid``,
    ``server_connection_id``) populated when present in the record.
    """
    rec = _unwrap_mongod_record(line)
    if rec is None or rec.get("c") != "COMMAND":
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
    return MongodCommand(
        timestamp=_mongod_record_timestamp(rec),
        pod=pod,
        command=cmd_name,
        namespace=ns,
        cursor_id=cursor_id,
        duration_ms=attr.get("durationMillis"),
        has_search_stage=has_search,
        lsid=_extract_lsid_hex(cmd_doc),
        server_connection_id=_extract_server_connection_id(rec.get("ctx")),
        raw=cmd_doc,
    )


def parse_mongod_network_log_line(pod: str, line: str) -> Optional[tuple[str, dict]]:
    """Parse one mongod NETWORK log line.

    Recognised records (verbosity 2):
      - ``id=7401401``  "Constructed a new gRPC egress session"
        → ``("SESSION_OPEN", {pod, timestamp, session_id, client_id, remote})``
      - ``id=7401403``  "Finished cleaning up a gRPC egress session"
        → ``("SESSION_CLOSE", {pod, timestamp, session_id, status})``

    Returns ``None`` for any other record. Backwards-compatible: when
    ``network`` verbosity is less than 2 these records aren't emitted
    at all and this parser simply doesn't fire.
    """
    rec = _unwrap_mongod_record(line)
    if rec is None or rec.get("c") != "NETWORK":
        return None
    log_id = rec.get("id")
    if log_id not in (_MONGOD_LOG_ID_SESSION_OPEN, _MONGOD_LOG_ID_SESSION_CLOSE):
        return None
    attr = rec.get("attr") or {}
    session = attr.get("session") or {}
    if not isinstance(session, dict):
        return None
    ts = _mongod_record_timestamp(rec)
    session_id = _maybe_int(session.get("id"))
    if session_id is None:
        return None
    client_id = _maybe_str(session.get("clientId"))
    remote = _maybe_str(session.get("remote"))
    if log_id == _MONGOD_LOG_ID_SESSION_OPEN:
        return (
            "SESSION_OPEN",
            {
                "pod": pod,
                "timestamp": ts,
                "session_id": session_id,
                "client_id": client_id,
                "remote": remote,
            },
        )
    return (
        "SESSION_CLOSE",
        {
            "pod": pod,
            "timestamp": ts,
            "session_id": session_id,
            "client_id": client_id,
            "remote": remote,
            "status": _maybe_str(attr.get("status")),
        },
    )


# ----------------------------------------------------------------------
# Aggregation
# ----------------------------------------------------------------------


def build_stream_summaries(
    log_sources: Iterable[str], namespace: str = "ls-0"
) -> tuple[dict[tuple[str, int], StreamSummary], list[dict]]:
    """Read mongot logs and return per-(pod, streamId) summaries plus a list of batch events.

    Tuple kinds emitted by ``parse_mongot_log_line`` other than ``BATCH``
    (``MONGOT_STREAM_OPEN``, ``MONGOT_CMD``) belong to the patched mongot
    surface and are collected separately by
    ``read_mongot_interceptor_events``. We skip them here so this function
    keeps its tight (summaries, batches) contract.
    """
    summaries: dict[tuple[str, int], StreamSummary] = {}
    batches: list[dict] = []
    for pod, line in iter_log_lines(log_sources, namespace=namespace):
        parsed = parse_mongot_log_line(pod, line)
        if parsed is None:
            continue
        if isinstance(parsed, tuple):
            if parsed[0] == "BATCH":
                batches.append(parsed[1])
            # else: MONGOT_STREAM_OPEN / MONGOT_CMD — captured elsewhere
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


def read_mongot_interceptor_events(
    log_sources: Iterable[str], namespace: str = "ls-0"
) -> tuple[list[dict], list[dict]]:
    """Collect the patched mongot's debug records: stream-open + per-command.

    Returns ``(stream_opens, commands)`` where each entry is the payload
    dict that ``parse_mongot_log_line`` produced (``timestamp``, ``pod``,
    ``client_id``, and on the command branch ``cursor_id`` + ``command``).
    With un-patched mongot (no ``MongoDbGrpcProtocolInterceptor`` DEBUG
    line, no ``cursorId`` key on the *Command logs) both lists are empty.
    """
    stream_opens: list[dict] = []
    commands: list[dict] = []
    for pod, line in iter_log_lines(log_sources, namespace=namespace):
        parsed = parse_mongot_log_line(pod, line)
        if not isinstance(parsed, tuple):
            continue
        kind, payload = parsed
        if kind == "MONGOT_STREAM_OPEN":
            stream_opens.append(payload)
        elif kind == "MONGOT_CMD":
            commands.append(payload)
    return stream_opens, commands


def read_mongod_commands(log_sources: Iterable[str], namespace: str = "ls-0") -> list[MongodCommand]:
    out: list[MongodCommand] = []
    for pod, line in iter_log_lines(log_sources, namespace=namespace):
        cmd = parse_mongod_log_line(pod, line)
        if cmd is not None:
            out.append(cmd)
    return out


def read_mongod_sessions(log_sources: Iterable[str], namespace: str = "ls-0") -> list[MongodSession]:
    """Build ``MongodSession`` objects from mongod NETWORK:2 log records.

    Each session is created on the SESSION_OPEN record and closed on the
    matching SESSION_CLOSE (matched by ``(pod, session_id)``). If a close
    record is missing (e.g. the test stopped capturing before cleanup),
    the session is still returned with ``closed_at=None``.
    """
    by_key: dict[tuple[str, int], MongodSession] = {}
    for pod, line in iter_log_lines(log_sources, namespace=namespace):
        ev = parse_mongod_network_log_line(pod, line)
        if ev is None:
            continue
        kind, payload = ev
        key = (payload["pod"], payload["session_id"])
        if kind == "SESSION_OPEN":
            by_key[key] = MongodSession(
                pod=payload["pod"],
                session_id=payload["session_id"],
                client_id=payload.get("client_id") or "",
                remote=payload.get("remote"),
                opened_at=payload["timestamp"],
            )
        else:  # SESSION_CLOSE
            sess = by_key.get(key)
            if sess is None:
                # CLOSE before OPEN — shouldn't happen, but be defensive
                # and surface the close record as a session with only the
                # closed_at populated.
                sess = MongodSession(
                    pod=payload["pod"],
                    session_id=payload["session_id"],
                    client_id=payload.get("client_id") or "",
                    remote=payload.get("remote"),
                    opened_at=payload["timestamp"],
                )
                by_key[key] = sess
            sess.closed_at = payload["timestamp"]
            sess.status = payload.get("status")
    return list(by_key.values())


def correlate_sessions_with_cursors(
    sessions: list[MongodSession],
    commands: list[MongodCommand],
) -> list[MongodSession]:
    """Fill in ``MongodSession.cursor_id`` by matching against COMMAND records.

    On a single mongod pod, an egress session owns the FIRST ``$search``
    aggregate record whose timestamp falls within the session's
    open/close window. The session's cursor_id is then derived as:

    - if that aggregate's ``cursor_id`` is set (mongod parsed it from
      the command response), use it directly; otherwise
    - look at the FIRST getMore on the SAME pod + SAME lsid after the
      aggregate, and take ITS cursor_id (every getMore on the cursor
      carries its id in the command doc).

    The fallback matters because mongod's aggregate-side COMMAND record
    today writes ``cursor.batchSize`` (the request) into ``attr.command``
    rather than the response ``cursor.id`` — see the parser at
    ``parse_mongod_log_line``. The getMore records DO carry cursor_id
    (it's the value passed as ``getMore``).

    Returns the same sessions list with ``cursor_id`` mutated in place.
    Sessions without a matching aggregate are left with cursor_id=None.
    """
    by_pod_search_agg: dict[str, list[MongodCommand]] = defaultdict(list)
    by_pod_lsid_getmore: dict[tuple[str, Optional[str]], list[MongodCommand]] = defaultdict(list)
    for c in commands:
        if c.timestamp is None:
            continue
        if c.has_search_stage and c.command == "aggregate":
            by_pod_search_agg[c.pod].append(c)
        elif c.command == "getMore" and c.cursor_id is not None:
            by_pod_lsid_getmore[(c.pod, c.lsid)].append(c)
    for lst in by_pod_search_agg.values():
        lst.sort(key=lambda c: c.timestamp)
    for lst in by_pod_lsid_getmore.values():
        lst.sort(key=lambda c: c.timestamp)
    for sess in sessions:
        if sess.opened_at is None:
            continue
        upper = sess.closed_at or datetime.max
        for c in by_pod_search_agg.get(sess.pod, []):
            if c.timestamp is None:
                continue
            if sess.opened_at <= c.timestamp <= upper:
                if c.cursor_id is not None:
                    sess.cursor_id = c.cursor_id
                else:
                    # Use the first getMore with same lsid after this
                    # aggregate to recover the cursor_id.
                    follow_ups = by_pod_lsid_getmore.get((sess.pod, c.lsid), [])
                    for g in follow_ups:
                        if g.timestamp is not None and g.timestamp >= c.timestamp:
                            sess.cursor_id = g.cursor_id
                            break
                break
    return sessions


# ----------------------------------------------------------------------
# Envoy debug-log parser
# ----------------------------------------------------------------------


def parse_envoy_debug_log(
    log_sources: Iterable[str], namespace: str = "ls-0"
) -> list[EnvoyStream]:
    """Walk envoy ``http2:debug``/``http:debug`` output and produce per-stream summaries.

    Envoy's runtime debug log is unstructured text emitted by the
    individual subsystems (http, http2, router, connection). The parser
    here is intentionally simple: we read top-to-bottom on a single pod's
    log, track the current connection-and-HCM-stream as headers come in,
    and fold subsequent header/DATA/close lines onto that record.

    Returned ``EnvoyStream`` records carry the ``client_id`` from the
    ``mongodb-clientid`` request header — the cross-side join key.

    NOT suitable for live log streams; this is a post-hoc analyzer. For
    one-shot capture, see ``_probe_envoy_debug_h2.py``.
    """
    # Per-pod state — envoy reuses (connection_id, stream_id) numbers
    # across pods of course, so we key on pod.
    streams_by_pod: dict[str, dict[tuple[int, int], EnvoyStream]] = defaultdict(dict)
    # Map (pod, connection_id, hcm_stream_id) -> wire stream_id so the
    # later DATA frames (which only carry wire stream_id, not HCM id)
    # can find their owning stream.
    hcm_to_wire: dict[tuple[str, int, int], int] = {}
    # Current "we just saw 'request headers complete' on this connection"
    # so subsequent indented header lines fold onto it. Indexed by pod.
    current_headers: dict[str, tuple[int, int]] = {}

    for pod, line in iter_log_lines(log_sources, namespace=namespace):
        ts = _parse_envoy_timestamp(line)
        conn_m = _ENVOY_CONN_RE.search(line)

        # 1) New stream / HCM-stream identification: lines like
        #    "[ConnectionId:X,StreamId:HCM_ID] request headers complete"
        if conn_m and "request headers complete" in line:
            cid = int(conn_m.group(1))
            hcm_sid = int(conn_m.group(2)) if conn_m.group(2) else None
            if hcm_sid is None:
                continue
            current_headers[pod] = (cid, hcm_sid)
            continue

        # 2) Continuation header line under the active headers-complete
        #    record. Pulls ``mongodb-clientid`` and ``:path`` onto the
        #    pending EnvoyStream record (we don't have the wire stream_id
        #    yet — we'll patch the dict entry when the first DATA frame
        #    fires below).
        hdr_m = _ENVOY_HEADER_LINE_RE.match(line)
        if hdr_m and pod in current_headers:
            cid, hcm_sid = current_headers[pod]
            key_pending = (cid, -hcm_sid)  # negative key for HCM-pending entries
            stream = streams_by_pod[pod].setdefault(
                key_pending,
                EnvoyStream(pod=pod, connection_id=cid, stream_id=-1, hcm_stream_id=hcm_sid),
            )
            header_name = hdr_m.group(1).lower()
            header_value = hdr_m.group(2)
            if header_name == "mongodb-clientid":
                stream.client_id = header_value
            elif header_name == ":path":
                stream.path = header_value
            elif header_name == "grpc-status":
                stream.grpc_status = header_value
            continue

        # 3) DATA-frame visibility from the codec:
        #    "[ConnectionId:X] Http2Visitor: remaining data payload: N, stream_id: W, end_stream: ..."
        if conn_m:
            cid = int(conn_m.group(1))
            data_m = _ENVOY_HTTP2_DATA_RE.search(line)
            if data_m:
                payload_bytes = int(data_m.group(1))
                wire_sid = int(data_m.group(2))
                end_stream = data_m.group(3) == "true"
                # Promote a pending HCM-only entry to the canonical
                # (connection_id, wire_sid) key on first DATA frame.
                key = (cid, wire_sid)
                stream = streams_by_pod[pod].get(key)
                if stream is None:
                    # Try to find the pending HCM entry on this connection
                    # and promote it.
                    promoted = None
                    for k, s in list(streams_by_pod[pod].items()):
                        if k[0] == cid and k[1] < 0 and s.stream_id == -1:
                            s.stream_id = wire_sid
                            streams_by_pod[pod].pop(k)
                            streams_by_pod[pod][key] = s
                            promoted = s
                            break
                    if promoted is None:
                        promoted = EnvoyStream(
                            pod=pod, connection_id=cid, stream_id=wire_sid
                        )
                        streams_by_pod[pod][key] = promoted
                    stream = promoted
                if stream.opened_at is None:
                    stream.opened_at = ts
                # Classify in/out based on connection direction is hard
                # from this single line; envoy emits both downstream and
                # upstream codec lines and they're indistinguishable
                # without the connection-direction context. Aggregate
                # everything as outbound for now (the analyzer just needs
                # a coarse byte/frame count).
                stream.outbound_data_frames += 1
                stream.outbound_bytes += payload_bytes
                if end_stream:
                    stream.closed_at = ts
                continue

            close_m = _ENVOY_STREAM_CLOSE_RE.search(line)
            if close_m:
                wire_sid = int(close_m.group(1))
                stream = streams_by_pod[pod].get((cid, wire_sid))
                if stream is not None:
                    stream.closed_at = ts
                continue

            if "RST_STREAM" in line:
                # The line format is variable here; try to capture the
                # wire stream id from the "stream X" pattern.
                m = re.search(r"stream (\d+)", line)
                if m:
                    wire_sid = int(m.group(1))
                    stream = streams_by_pod[pod].get((cid, wire_sid))
                    if stream is not None:
                        stream.rst_stream = True
                        stream.closed_at = ts

    # Flatten and drop any HCM-only pending entries (no DATA frame ever
    # arrived — usually means we caught the tail of an in-flight stream).
    out: list[EnvoyStream] = []
    for pod, by_key in streams_by_pod.items():
        for key, stream in by_key.items():
            if stream.stream_id < 0:
                continue
            out.append(stream)
    return out


def _parse_envoy_timestamp(line: str) -> Optional[datetime]:
    """Parse the leading ``[hh:mm:ss.mmm]`` timestamp from an envoy debug line.

    Envoy debug logs use clock-only timestamps (no date) — to keep the
    timeline ordering meaningful we attach today's date in UTC. Tests
    that need wall-clock precision should capture from a known
    wall-clock anchor.
    """
    m = _ENVOY_TS_RE.search(line)
    if not m:
        return None
    date_part, time_part = m.group(1), m.group(2)
    if date_part:
        try:
            return datetime.fromisoformat(f"{date_part}T{time_part}+00:00")
        except ValueError:
            return None
    # Use today's UTC date as anchor (best effort).
    today = datetime.utcnow().date()
    try:
        return datetime.fromisoformat(f"{today.isoformat()}T{time_part}+00:00")
    except ValueError:
        return None


# ----------------------------------------------------------------------
# pymongo CommandListener event parser
# ----------------------------------------------------------------------


def parse_client_wire_ops(events: list[Any], *, anchor_wall_time: Optional[datetime] = None) -> list[ClientWireOp]:
    """Convert a list of CommandListener records into ``ClientWireOp`` dataclasses.

    Accepts either ``connectivity.ClientWireOp`` instances (the in-process
    type — duck-typed by attribute name) OR plain dicts with the same
    field names. Returns a uniform list of analyzer-side ``ClientWireOp``.

    ``anchor_wall_time`` lets callers anchor the captured ``time.monotonic()``
    timestamps to wall-clock so the timeline can interleave them with
    log-parsed events. We anchor the FIRST event to ``anchor_wall_time``
    and add ``(this_timestamp - first_timestamp)`` to it, preserving the
    in-process relative ordering. If omitted, the analyzer just treats
    the monotonic values as relative seconds since some unspecified
    origin and produces a ``datetime`` 1970-01-01 + that offset.
    """
    # Pre-pass: find the smallest monotonic value so we can anchor every
    # subsequent record to ``anchor_wall_time + (ts - min_ts)``. Without
    # the delta the whole client side collapses to a single wall-clock
    # instant in the rendered timeline.
    monotonic_min: Optional[float] = None
    for ev in events:
        ts_raw = _to_dict(ev).get("timestamp")
        if isinstance(ts_raw, (int, float)):
            if monotonic_min is None or ts_raw < monotonic_min:
                monotonic_min = float(ts_raw)
    out: list[ClientWireOp] = []
    for ev in events:
        rec = _to_dict(ev)
        ts_raw = rec.get("timestamp")
        if isinstance(ts_raw, datetime):
            ts = ts_raw
        elif isinstance(ts_raw, (int, float)) and anchor_wall_time is not None and monotonic_min is not None:
            from datetime import timedelta

            ts = anchor_wall_time + timedelta(seconds=float(ts_raw) - monotonic_min)
        elif isinstance(ts_raw, (int, float)):
            # No anchor — treat as seconds-since-epoch placeholder.
            try:
                ts = datetime.utcfromtimestamp(float(ts_raw))
            except (OverflowError, OSError, ValueError):
                ts = datetime(1970, 1, 1)
        else:
            ts = datetime(1970, 1, 1)
        out.append(
            ClientWireOp(
                phase=rec.get("phase", "?"),
                command_name=rec.get("command_name", "?"),
                request_id=int(rec.get("request_id", 0) or 0),
                timestamp=ts,
                server_connection_id=rec.get("server_connection_id"),
                lsid=rec.get("lsid"),
                cursor_id=rec.get("cursor_id"),
                duration_micros=rec.get("duration_micros"),
                n_returned=rec.get("n_returned"),
                database_name=rec.get("database_name"),
                operation_id=rec.get("operation_id"),
                failure=rec.get("failure"),
            )
        )
    return out


def _to_dict(obj: Any) -> dict:
    if isinstance(obj, dict):
        return obj
    # Duck-type: dataclasses and other simple records expose __dict__.
    if hasattr(obj, "__dict__"):
        return obj.__dict__
    return {}


# ----------------------------------------------------------------------
# Unified timeline
# ----------------------------------------------------------------------


def unified_timeline(
    *,
    client_ops: Optional[list[ClientWireOp]] = None,
    mongod_sessions: Optional[list[MongodSession]] = None,
    mongod_commands: Optional[list[MongodCommand]] = None,
    envoy_streams: Optional[list[EnvoyStream]] = None,
    mongot_streams: Optional[dict[tuple[str, int], StreamSummary]] = None,
    mongot_batches: Optional[list[dict]] = None,
    mongot_extras: Optional[list[tuple[str, dict]]] = None,
) -> list[TimelineEvent]:
    """Merge cross-layer events into a chronologically-ordered timeline.

    Each input is optional — callers can pass only the layers they have
    data for. Join keys are filled where deterministic data exists:

    - ``client_id`` (UUID) links mongod sessions to envoy streams (and,
      when mongot is patched, to mongot stream-open records).
    - ``lsid`` + ``server_connection_id`` link client wire ops to
      mongod commands.
    - ``cursor_id`` links client wire ops + mongod commands +
      (post-mongot-patch) mongot command logs and batches.

    Gaps (envoy ↔ mongot, mongod ↔ mongot today, without the mongot
    patch) are surfaced as ``client_id=None`` / ``stream_id=None`` on
    the affected events — callers can render those gaps explicitly
    rather than silently lose them.
    """
    events: list[TimelineEvent] = []

    for op in client_ops or []:
        events.append(
            TimelineEvent(
                timestamp=op.timestamp,
                layer="client",
                pod=None,
                kind=f"{op.command_name}.{op.phase}",
                lsid=op.lsid,
                cursor_id=op.cursor_id,
                server_connection_id=op.server_connection_id,
                details={
                    "request_id": op.request_id,
                    "duration_micros": op.duration_micros,
                    "n_returned": op.n_returned,
                    "failure": op.failure,
                },
            )
        )

    for sess in mongod_sessions or []:
        if sess.opened_at is not None:
            events.append(
                TimelineEvent(
                    timestamp=sess.opened_at,
                    layer="mongod.net",
                    pod=sess.pod,
                    kind="session_open",
                    client_id=sess.client_id or None,
                    cursor_id=sess.cursor_id,
                    session_id=sess.session_id,
                    details={"remote": sess.remote},
                )
            )
        if sess.closed_at is not None:
            events.append(
                TimelineEvent(
                    timestamp=sess.closed_at,
                    layer="mongod.net",
                    pod=sess.pod,
                    kind="session_close",
                    client_id=sess.client_id or None,
                    cursor_id=sess.cursor_id,
                    session_id=sess.session_id,
                    details={"status": sess.status, "remote": sess.remote},
                )
            )

    for cmd in mongod_commands or []:
        if cmd.timestamp is None:
            continue
        events.append(
            TimelineEvent(
                timestamp=cmd.timestamp,
                layer="mongod.cmd",
                pod=cmd.pod,
                kind=cmd.command,
                lsid=cmd.lsid,
                cursor_id=cmd.cursor_id,
                server_connection_id=cmd.server_connection_id,
                details={
                    "namespace": cmd.namespace,
                    "duration_ms": cmd.duration_ms,
                    "has_search_stage": cmd.has_search_stage,
                },
            )
        )

    for stream in envoy_streams or []:
        if stream.opened_at is not None:
            events.append(
                TimelineEvent(
                    timestamp=stream.opened_at,
                    layer="envoy",
                    pod=stream.pod,
                    kind="stream_open",
                    client_id=stream.client_id,
                    stream_id=stream.stream_id,
                    details={
                        "path": stream.path,
                        "connection_id": stream.connection_id,
                        "hcm_stream_id": stream.hcm_stream_id,
                    },
                )
            )
        if stream.closed_at is not None:
            events.append(
                TimelineEvent(
                    timestamp=stream.closed_at,
                    layer="envoy",
                    pod=stream.pod,
                    kind="stream_close",
                    client_id=stream.client_id,
                    stream_id=stream.stream_id,
                    details={
                        "grpc_status": stream.grpc_status,
                        "rst_stream": stream.rst_stream,
                        "outbound_bytes": stream.outbound_bytes,
                        "outbound_data_frames": stream.outbound_data_frames,
                    },
                )
            )

    for (pod, sid), summary in (mongot_streams or {}).items():
        if summary.opened_at is not None:
            events.append(
                TimelineEvent(
                    timestamp=summary.opened_at,
                    layer="mongot.frame",
                    pod=pod,
                    kind="stream_open",
                    stream_id=sid,
                    details={"grpc_path": summary.grpc_path, "peer": summary.peer},
                )
            )
        if summary.closed_at is not None:
            events.append(
                TimelineEvent(
                    timestamp=summary.closed_at,
                    layer="mongot.frame",
                    pod=pod,
                    kind="stream_close",
                    stream_id=sid,
                    details={
                        "grpc_status": summary.grpc_status,
                        "rst_stream": summary.rst_stream,
                        "outbound_bytes": summary.outbound_bytes,
                        "outbound_data_frames": summary.outbound_data_frames,
                    },
                )
            )

    for batch in mongot_batches or []:
        ts = batch.get("timestamp")
        if not isinstance(ts, datetime):
            continue
        events.append(
            TimelineEvent(
                timestamp=ts,
                layer="mongot.batch",
                pod=batch.get("pod"),
                kind="batch_prepared",
                # cursor_id / client_id are populated only when the
                # mongot patch is in place; left None otherwise.
                cursor_id=batch.get("cursor_id"),
                client_id=batch.get("client_id"),
                details={"size": batch.get("size")},
            )
        )

    # Optional mongot extras (MONGOT_STREAM_OPEN / MONGOT_CMD) — produced
    # by ``parse_mongot_log_line`` only when the proposed mongot patches
    # are applied. Pre-patch this list will be empty.
    for kind, payload in mongot_extras or []:
        ts = payload.get("timestamp")
        if not isinstance(ts, datetime):
            continue
        if kind == "MONGOT_STREAM_OPEN":
            events.append(
                TimelineEvent(
                    timestamp=ts,
                    layer="mongot.frame",
                    pod=payload.get("pod"),
                    kind="interceptor_stream_open",
                    client_id=payload.get("client_id"),
                    details={"path": payload.get("path")},
                )
            )
        elif kind == "MONGOT_CMD":
            events.append(
                TimelineEvent(
                    timestamp=ts,
                    layer="mongot.batch",
                    pod=payload.get("pod"),
                    kind=payload.get("command", "command"),
                    cursor_id=payload.get("cursor_id"),
                    client_id=payload.get("client_id"),
                )
            )

    events.sort(key=lambda e: (e.timestamp, e.layer, e.pod or ""))
    return events


# ----------------------------------------------------------------------
# Cursor-tree view
# ----------------------------------------------------------------------


def build_cursor_trees(
    client_ops: list[ClientWireOp],
    mongod_sessions: list[MongodSession],
    mongod_commands: list[MongodCommand],
    envoy_streams: list[EnvoyStream],
    mongot_streams: dict[tuple[str, int], StreamSummary],
    mongot_batches: list[dict],
    mongot_stream_opens: Optional[list[dict]] = None,
    mongot_cmds: Optional[list[dict]] = None,
) -> list[CursorTree]:
    """Group cross-layer events under per-cursor trees.

    Join rules (derived from the empirical correlations verified in
    ``tmp/search-caching-investigation/observability-followup.md``):

    1. **client → cursor_tree**: pair ``CommandListener`` started+succeeded
       records by ``request_id``. The aggregate's ``started`` record has
       no ``cursor_id`` (mongod allocates it on the response); we
       back-fill the cursor_id from the matching ``succeeded`` record so
       every wire op has a known cursor id by the time we group.
    2. **client lsid → mongod_cmd**: lsid hex must match
       ``MongodCommand.lsid`` (which mongod prints under COMMAND:2). We
       also use cursor_id where the client surfaces one (getMore /
       killCursors) to disambiguate when one client has multiple cursors
       in flight.
    3. **aggregate succeed → mongod_session_open**: the session is opened
       on the primary mongod when the cursor is established. We take the
       session whose ``opened_at`` is within ``[client_started -
       slack, client_succeeded + slack]`` AND that resolves to this
       cursor_id via ``correlate_sessions_with_cursors``.
    4. **mongod_session.client_id → envoy_stream.client_id**: exact UUID
       match (the load-bearing envoy↔mongod join).
    5. **envoy_stream.client_id → mongot_stream_open.client_id**: exact
       UUID match when the mongot interceptor patch is in place; fall
       back to "first mongot stream open on any pod within ±1s of the
       envoy stream open" otherwise.
    6. **mongot_stream_open → StreamSummary**: same pod + same
       ``/mongodb.CommandService/…`` path. Only one ``$search``-style
       stream per pod is open at a time on this dev cluster so we use
       the first match by ``opened_at``.
    7. **per-getMore → mongot.batch?**: if a ``LuceneSearchBatchProducer``
       event landed inside this op's ``[client_started - slack,
       client_succeeded + slack]`` on the SAME mongot pod as the
       cursor's stream, the op caused a fresh mongot pull. Otherwise
       the page was served from the mongod-side cache.
    8. **killCursors → session_close**: same pod + same session id as
       the cursor's aggregate; falls within ±5s of the killCursors.

    Returns a list of ``CursorTree``, sorted by the cursor's first wire op.
    Cursors that did NOT issue a ``$search`` (e.g. internal oplog tailers
    surfaced by mongod's COMMAND:2 noise) are filtered out: a tree is
    emitted only if it contains at least one aggregate wire op AND that
    aggregate's mongod command record carries ``has_search_stage=True``.
    """
    mongot_stream_opens = mongot_stream_opens or []
    mongot_cmds = mongot_cmds or []

    # 1) Collapse client wire ops by (request_id) into started+succeeded
    # pairs. For each pair, surface cursor_id from whichever phase has it.
    by_req: dict[int, dict[str, ClientWireOp]] = defaultdict(dict)
    for op in client_ops:
        if op.phase in ("started", "succeeded", "failed"):
            by_req[op.request_id][op.phase] = op

    collapsed: list[CursorTreeWireOp] = []
    for req_id, phases in by_req.items():
        started = phases.get("started")
        succeeded = phases.get("succeeded") or phases.get("failed")
        if started is None and succeeded is None:
            continue
        sample = started or succeeded  # for static fields
        # Cursor id can come from either phase: pymongo populates it on
        # ``started`` for getMore/killCursors (from the command doc) and
        # on ``succeeded`` for aggregate (from reply.cursor.id).
        cursor_id = None
        for phase in (started, succeeded):
            if phase is not None and phase.cursor_id is not None:
                cursor_id = phase.cursor_id
                break
        collapsed.append(
            CursorTreeWireOp(
                command_name=sample.command_name,
                request_id=req_id,
                client_started=(started.timestamp if started else None),
                client_succeeded=(succeeded.timestamp if succeeded else None),
                duration_micros=(succeeded.duration_micros if succeeded else None),
                n_returned=(succeeded.n_returned if succeeded else None),
                server_connection_id=sample.server_connection_id
                or (succeeded.server_connection_id if succeeded else None),
                lsid=sample.lsid or (succeeded.lsid if succeeded else None),
                cursor_id=cursor_id,
                failure=(succeeded.failure if succeeded and succeeded.phase == "failed" else None),
            )
        )

    # Filter to wire ops we care about + that have a usable cursor_id.
    # The aggregate that opens a cursor is the one wire op whose
    # cursor_id is set ONLY on succeeded, so we keep it regardless.
    relevant_cmds = {"aggregate", "getMore", "killCursors"}
    collapsed = [op for op in collapsed if op.command_name in relevant_cmds]

    # 2) Build per-cursor groups. Aggregate is the cursor opener; its
    # succeeded.cursor_id is the cursor's id. getMore/killCursors quote
    # the same cursor_id.
    trees_by_cursor: dict[int, CursorTree] = {}
    for op in collapsed:
        if op.cursor_id is None:
            continue
        tree = trees_by_cursor.setdefault(
            op.cursor_id,
            CursorTree(cursor_id=op.cursor_id, client_lsid=op.lsid),
        )
        # Use the lsid from any op that has it (every op on a cursor
        # shares the same lsid).
        if tree.client_lsid is None and op.lsid is not None:
            tree.client_lsid = op.lsid
        tree.wire_ops.append(op)

    # 3) Index lookups for the lower-layer joins.
    cmds_by_lsid_cursor: dict[tuple[Optional[str], int], list[MongodCommand]] = defaultdict(list)
    cmds_by_cursor: dict[int, list[MongodCommand]] = defaultdict(list)
    # Aggregate records carry cursor_id=None (mongod allocates it on the
    # response, our parser reads from the request command doc which only
    # has cursor.batchSize). Match them by (lsid, command=aggregate,
    # has_search_stage=True) — this is the unambiguous key on a single
    # client because pymongo issues exactly one aggregate per cursor.
    search_aggs_by_lsid: dict[str, list[MongodCommand]] = defaultdict(list)
    for cmd in mongod_commands:
        if cmd.cursor_id is not None:
            cmds_by_cursor[cmd.cursor_id].append(cmd)
            cmds_by_lsid_cursor[(cmd.lsid, cmd.cursor_id)].append(cmd)
        elif (
            cmd.command == "aggregate" and cmd.has_search_stage and cmd.lsid
        ):
            search_aggs_by_lsid[cmd.lsid].append(cmd)
    for lst in cmds_by_lsid_cursor.values():
        lst.sort(key=lambda c: c.timestamp or datetime.max)
    for lst in cmds_by_cursor.values():
        lst.sort(key=lambda c: c.timestamp or datetime.max)
    for lst in search_aggs_by_lsid.values():
        lst.sort(key=lambda c: c.timestamp or datetime.max)

    # Mongod sessions indexed by their cursor_id (filled by
    # correlate_sessions_with_cursors). client_id UUID is the join key
    # outward to envoy / mongot.
    sessions_by_cursor: dict[int, MongodSession] = {}
    for sess in mongod_sessions:
        if sess.cursor_id is not None and sess.cursor_id not in sessions_by_cursor:
            sessions_by_cursor[sess.cursor_id] = sess

    # Envoy streams by their client_id UUID.
    envoy_by_client_id: dict[str, EnvoyStream] = {}
    for es in envoy_streams:
        if es.client_id and es.client_id not in envoy_by_client_id:
            envoy_by_client_id[es.client_id] = es

    # Mongot stream-open records (patched mongot interceptor) by client_id.
    mongot_open_by_client_id: dict[str, dict] = {}
    for ev in mongot_stream_opens:
        cid = ev.get("client_id")
        if cid and cid not in mongot_open_by_client_id:
            mongot_open_by_client_id[cid] = ev

    # Mongot StreamSummary by pod + grpc_path (the gRPC method name).
    summaries_by_pod_path: dict[tuple[str, str], list[StreamSummary]] = defaultdict(list)
    for (pod, _sid), summary in mongot_streams.items():
        if summary.grpc_path:
            # Mongot logs path with leading slash; patched interceptor
            # may log it without. Strip the slash so both representations
            # collide on the same key.
            normalized = summary.grpc_path.lstrip("/")
            summaries_by_pod_path[(pod, normalized)].append(summary)
    for lst in summaries_by_pod_path.values():
        lst.sort(key=lambda s: s.opened_at or datetime.max)

    # 4) Fill in lower-layer fields per tree.
    for tree in trees_by_cursor.values():
        tree.wire_ops.sort(
            key=lambda op: op.client_started or op.client_succeeded or datetime.max
        )

        # Tree-level joins start from the mongod session
        session = sessions_by_cursor.get(tree.cursor_id)
        if session is not None:
            tree.mongod_pod = session.pod
            tree.client_id_uuid = session.client_id or None
        if tree.client_id_uuid is None:
            # Fallback: any mongod_session whose opened_at is within
            # ±5s of the aggregate's client_started. Rare — only fires
            # when correlate_sessions_with_cursors couldn't pin the
            # cursor (e.g. session was opened before the aggregate
            # landed in COMMAND log).
            agg = _first_op(tree.wire_ops, "aggregate")
            if agg is not None and agg.client_started is not None:
                for sess in mongod_sessions:
                    if (
                        sess.opened_at is not None
                        and abs((sess.opened_at - agg.client_started).total_seconds()) < 5.0
                    ):
                        tree.mongod_pod = tree.mongod_pod or sess.pod
                        tree.client_id_uuid = sess.client_id or None
                        session = sess
                        break

        # Envoy stream by client_id UUID
        envoy_stream = (
            envoy_by_client_id.get(tree.client_id_uuid) if tree.client_id_uuid else None
        )

        # Mongot stream-open (patched interceptor) by client_id
        mongot_open = (
            mongot_open_by_client_id.get(tree.client_id_uuid)
            if tree.client_id_uuid
            else None
        )
        if mongot_open is None and envoy_stream is not None and envoy_stream.opened_at:
            # Fallback: first mongot stream-open within ±1s of the envoy
            # stream open.
            for ev in mongot_stream_opens:
                ts = ev.get("timestamp")
                if isinstance(ts, datetime) and abs(
                    (ts - envoy_stream.opened_at).total_seconds()
                ) < 1.0:
                    mongot_open = ev
                    break

        # Mongot StreamSummary by pod + grpc_path
        mongot_stream_summary: Optional[StreamSummary] = None
        if mongot_open is not None:
            pod = mongot_open.get("pod")
            path = (mongot_open.get("path") or "").lstrip("/")
            candidates = summaries_by_pod_path.get((pod, path), []) if pod else []
            # Pick the first whose opened_at is within ±2s of the
            # interceptor's timestamp.
            mopen_ts = mongot_open.get("timestamp")
            for s in candidates:
                if s.opened_at is None or not isinstance(mopen_ts, datetime):
                    mongot_stream_summary = s
                    break
                if abs((s.opened_at - mopen_ts).total_seconds()) < 2.0:
                    mongot_stream_summary = s
                    break
        elif envoy_stream is not None and envoy_stream.opened_at:
            # No patched interceptor — fall back to time-based join
            # against all summaries.
            best: Optional[StreamSummary] = None
            best_delta = float("inf")
            for (pod, _sid), summary in mongot_streams.items():
                if summary.opened_at is None:
                    continue
                delta = abs(
                    (summary.opened_at - envoy_stream.opened_at).total_seconds()
                )
                if delta < best_delta and delta < 2.0:
                    best = summary
                    best_delta = delta
            mongot_stream_summary = best

        if mongot_stream_summary is not None:
            tree.mongot_pod = mongot_stream_summary.pod
            tree.mongot_stream_id = mongot_stream_summary.stream_id

        # Mongot batches on the cursor's stream pod, sorted by time.
        mongot_pod_for_batches = tree.mongot_pod
        cursor_batches: list[dict] = []
        if mongot_pod_for_batches is not None:
            cursor_batches = sorted(
                [
                    b
                    for b in mongot_batches
                    if b.get("pod") == mongot_pod_for_batches
                    and isinstance(b.get("timestamp"), datetime)
                ],
                key=lambda b: b["timestamp"],
            )

        # Restrict batches to the cursor's lifetime (stream open .. close)
        if mongot_stream_summary is not None:
            life_start = mongot_stream_summary.opened_at or datetime.min
            life_end = mongot_stream_summary.closed_at or datetime.max
            cursor_batches = [
                b for b in cursor_batches if life_start <= b["timestamp"] <= life_end
            ]

        # Now distribute per-wire-op:
        for op in tree.wire_ops:
            # mongod_cmd: aggregate records carry cursor_id=None on the
            # mongod side, so the aggregate wire op matches on lsid alone.
            # getMore / killCursors prefer (lsid, cursor_id) and fall back
            # to cursor_id only.
            if op.command_name == "aggregate" and op.lsid is not None:
                candidates = search_aggs_by_lsid.get(op.lsid, [])
                op.mongod_cmd = _closest_within(
                    candidates, op.client_started, op.client_succeeded
                )
            else:
                if op.lsid is not None and op.cursor_id is not None:
                    candidates = [
                        c
                        for c in cmds_by_lsid_cursor.get((op.lsid, op.cursor_id), [])
                        if c.command == op.command_name
                    ]
                    op.mongod_cmd = _closest_within(
                        candidates, op.client_started, op.client_succeeded
                    )
                if op.mongod_cmd is None and op.cursor_id is not None:
                    candidates = [
                        c
                        for c in cmds_by_cursor.get(op.cursor_id, [])
                        if c.command == op.command_name
                    ]
                    op.mongod_cmd = _closest_within(
                        candidates, op.client_started, op.client_succeeded
                    )

            # session_open / envoy_stream / mongot_stream_open all hang
            # off the aggregate (cursor opener) only.
            if op.command_name == "aggregate":
                op.mongod_session_open = session
                op.envoy_stream = envoy_stream
                op.mongot_stream_open = mongot_open
                # mongot.cmd record (patched) — SearchCommand with this cursor_id
                for ev in mongot_cmds:
                    if (
                        ev.get("command") == "SearchCommand"
                        and ev.get("cursor_id") == op.cursor_id
                    ):
                        op.mongot_cmd = ev
                        break

            # killCursors closes the session
            if op.command_name == "killCursors":
                if session is not None and session.closed_at is not None:
                    op.mongod_session_close = session
                if mongot_stream_summary is not None and mongot_stream_summary.closed_at:
                    op.mongot_stream_close = mongot_stream_summary

            # Per-op mongot batch matching ("(cached)" predicate).
            # An op caused a fresh mongot pull iff a batch_prepared event
            # landed inside its wire window on this cursor's mongot pod.
            if op.client_started is not None and op.client_succeeded is not None:
                lo = op.client_started - _slack()
                hi = op.client_succeeded + _slack()
                matched = [
                    b for b in cursor_batches if lo <= b["timestamp"] <= hi
                ]
                if matched:
                    op.mongot_batches.extend(matched)
                    op.served_fresh_from_mongot = True

    # 5) Filter to $search trees only: a tree must have an aggregate
    # whose mongod_cmd has has_search_stage=True.
    final: list[CursorTree] = []
    for tree in trees_by_cursor.values():
        agg = _first_op(tree.wire_ops, "aggregate")
        if agg is None:
            continue
        if agg.mongod_cmd is None or not agg.mongod_cmd.has_search_stage:
            # If we couldn't match the aggregate to its mongod log line
            # (e.g. COMMAND verbosity wasn't bumped), keep the tree as
            # long as it has a mongot stream — that's the next-best
            # evidence the cursor talked to mongot.
            if tree.mongot_pod is None:
                continue
        final.append(tree)

    # Sort by the tree's first wire op timestamp for deterministic output.
    final.sort(
        key=lambda t: _first_wire_op_time(t) or datetime.max
    )
    return final


def _slack() -> Any:
    """Return the timedelta slack used for mongot-batch / session matching."""
    from datetime import timedelta

    return timedelta(seconds=_MONGOT_BATCH_MATCH_SLACK_SECONDS)


def _first_op(wire_ops: list[CursorTreeWireOp], command_name: str) -> Optional[CursorTreeWireOp]:
    for op in wire_ops:
        if op.command_name == command_name:
            return op
    return None


def _first_wire_op_time(tree: CursorTree) -> Optional[datetime]:
    for op in tree.wire_ops:
        if op.client_started is not None:
            return op.client_started
        if op.client_succeeded is not None:
            return op.client_succeeded
    return None


def _closest_within(
    candidates: list[MongodCommand],
    lo: Optional[datetime],
    hi: Optional[datetime],
) -> Optional[MongodCommand]:
    """Return the candidate whose timestamp is closest to ``[lo, hi]``.

    Mongod logs have ms precision while the client side captures
    monotonic timestamps anchored to wall clock — exact-equality
    matching isn't reliable. We accept anything within ±1s of the
    [lo, hi] window and prefer the closest match. Returns None if no
    candidate falls in the window.
    """
    if not candidates:
        return None
    if lo is None and hi is None:
        return candidates[0]
    from datetime import timedelta

    tol = timedelta(seconds=1.0)
    lo_eff = (lo or datetime.min) - tol
    hi_eff = (hi or datetime.max) + tol
    best = None
    best_delta = float("inf")
    for c in candidates:
        if c.timestamp is None:
            continue
        if not (lo_eff <= c.timestamp <= hi_eff):
            continue
        center = lo or hi or c.timestamp
        delta = abs((c.timestamp - center).total_seconds())
        if delta < best_delta:
            best = c
            best_delta = delta
    return best


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


def print_unified_timeline(events: list[TimelineEvent], *, max_events: int = 200) -> None:
    """Render a unified-timeline view of cross-layer events.

    Layers appear interleaved by ``timestamp``. Each line surfaces the
    join keys we extracted (``client_id``, ``lsid``, ``cursor_id``,
    ``server_connection_id``, ``stream_id``, ``session_id``) so a reader
    can eyeball which keys actually carried across the layers in this run.

    ``max_events`` caps the output for huge captures; pass ``0`` to print
    every event.
    """
    print(f"\n=== unified cross-layer timeline — {len(events)} event(s) ===")
    if not events:
        return
    head = events if max_events <= 0 else events[:max_events]
    for ev in head:
        ts = ev.timestamp.isoformat() if ev.timestamp else "?"
        pieces = [ts, f"{ev.layer:<13}", f"pod={ev.pod or '-'}", f"kind={ev.kind}"]
        for label, value in (
            ("client_id", ev.client_id),
            ("lsid", ev.lsid),
            ("cursor_id", ev.cursor_id),
            ("conn_id", ev.server_connection_id),
            ("stream_id", ev.stream_id),
            ("session_id", ev.session_id),
        ):
            if value is not None:
                pieces.append(f"{label}={value}")
        if ev.details:
            extras = " ".join(f"{k}={v}" for k, v in ev.details.items() if v is not None)
            if extras:
                pieces.append(extras)
        print("  " + "  ".join(pieces))
    if max_events and len(events) > max_events:
        print(f"  ... ({len(events) - max_events} more event(s) elided)")


# Internal tree node used by ``print_cursor_trees`` for rendering. Each
# wire op becomes a forest of these and we walk it with a single
# recursive renderer so the box-drawing prefixes nest correctly at
# arbitrary depth.
@dataclass
class _RenderNode:
    label: str
    children: list["_RenderNode"] = field(default_factory=list)


def print_cursor_trees(trees: list[CursorTree]) -> None:
    """Render per-cursor trees using box-drawing characters.

    One tree per ``$search`` cursor. The shape:

        cursor <id>  lsid=…  mongod=<pod>  mongot=<pod>  clientId=<uuid>  streamId=<n>
        ├─ client.aggregate     req_id=… +0.000s  dur=…ms  n=…
        │   └─ mongod.cmd.aggregate  ns=…  dur_ms=…  (has_search_stage)
        │       └─ mongod.net.session_open  session_id=…  client_id=…
        │           └─ envoy.stream  stream_id=…  client_id=…
        │               └─ mongot.stream_open  streamId=…  path=…
        │                   ├─ mongot.batch  size=…
        │                   └─ mongot.cmd.SearchCommand  cursorId=…   (patched only)
        ├─ client.getMore       req_id=… +0.157s  dur=…ms  n=…   ← FRESH MONGOT PULL (batch=…)
        │   └─ mongod.cmd.getMore  cursor_id=…  dur_ms=…
        │       └─ mongot.batch  size=…
        ├─ client.getMore       req_id=… +0.214s  dur=…ms  n=…   (cached)
        │   └─ mongod.cmd.getMore  cursor_id=…  dur_ms=…
        └─ client.killCursors   req_id=…
            └─ mongod.cmd.killCursors  cursor_id=…
                └─ mongod.net.session_close  client_id=…  status=…
                    └─ mongot.stream_close   streamId=…  status=…  rst=…
    """
    print(f"\n=== per-cursor tree view — {len(trees)} cursor(s) ===")
    if not trees:
        return

    for tree in trees:
        anchor = _first_wire_op_time(tree)
        header_bits = [f"cursor {tree.cursor_id}"]
        if tree.client_lsid:
            header_bits.append(f"lsid={tree.client_lsid[:16]}…")
        header_bits.append(f"mongod={_short(tree.mongod_pod)}")
        header_bits.append(f"mongot={_short(tree.mongot_pod)}")
        if tree.client_id_uuid:
            header_bits.append(f"clientId={tree.client_id_uuid}")
        if tree.mongot_stream_id is not None:
            header_bits.append(f"streamId={tree.mongot_stream_id}")
        print()
        print("  " + "  ".join(header_bits))

        n_ops = len(tree.wire_ops)
        for i, op in enumerate(tree.wire_ops):
            node = _wire_op_to_node(op, anchor, tree=tree)
            _render_node(node, prefix="", is_last=(i == n_ops - 1))


def _render_node(node: _RenderNode, *, prefix: str, is_last: bool) -> None:
    """Recursive box-drawing renderer; one call per visible line."""
    branch = "└─ " if is_last else "├─ "
    print(f"  {prefix}{branch}{node.label}")
    child_prefix = prefix + ("    " if is_last else "│   ")
    n = len(node.children)
    for i, child in enumerate(node.children):
        _render_node(child, prefix=child_prefix, is_last=(i == n - 1))


def _wire_op_to_node(
    op: CursorTreeWireOp, anchor: Optional[datetime], *, tree: Optional[CursorTree] = None
) -> _RenderNode:
    """Convert one ``CursorTreeWireOp`` to the renderer's node tree.

    ``tree`` is passed so the mongot.stream_open node can label its
    streamId — the streamId lives on the cursor tree, not on the per-op
    record.
    """
    # ---- root client.<cmd> label ----
    offset = ""
    if anchor and op.client_started:
        offset = f"+{(op.client_started - anchor).total_seconds():0.3f}s"
    elif anchor and op.client_succeeded:
        offset = f"+{(op.client_succeeded - anchor).total_seconds():0.3f}s"
    dur = "?" if op.duration_micros is None else f"{op.duration_micros / 1000:0.0f}ms"
    n = "?" if op.n_returned is None else str(op.n_returned)
    tag = ""
    if op.command_name == "getMore":
        if op.served_fresh_from_mongot:
            sizes = ",".join(str(b.get("size")) for b in op.mongot_batches)
            tag = f"   ← FRESH MONGOT PULL (batch={sizes})"
        else:
            tag = "   (cached)"
    pieces = [
        f"client.{op.command_name}",
        f"req_id={op.request_id}",
    ]
    if op.server_connection_id is not None:
        pieces.append(f"conn_id={op.server_connection_id}")
    if offset:
        pieces.append(offset)
    pieces.append(f"dur={dur}")
    pieces.append(f"n={n}")
    label = "  ".join(pieces) + tag
    root = _RenderNode(label=label)

    # ---- mongod.cmd.<cmd> ----
    if op.mongod_cmd is None:
        # No matched mongod record (e.g. COMMAND verbosity not at 2,
        # or the client op failed). Surface that as a leaf so a reader
        # can see the layer was checked.
        if op.command_name in {"aggregate", "getMore", "killCursors"}:
            root.children.append(_RenderNode(label="mongod.cmd  (no match)"))
        return root

    cmd = op.mongod_cmd
    cmd_pieces = [f"mongod.cmd.{cmd.command}"]
    if cmd.namespace:
        cmd_pieces.append(f"ns={cmd.namespace}")
    if cmd.duration_ms is not None:
        cmd_pieces.append(f"dur_ms={cmd.duration_ms}")
    if cmd.cursor_id is not None and op.command_name != "aggregate":
        cmd_pieces.append(f"cursor_id={cmd.cursor_id}")
    if cmd.has_search_stage:
        cmd_pieces.append("(has_search_stage)")
    cmd_node = _RenderNode(label="  ".join(cmd_pieces))
    root.children.append(cmd_node)

    # ---- mongod.net.session_open (aggregate only) ----
    if op.mongod_session_open is not None:
        sess = op.mongod_session_open
        net_open_label = (
            f"mongod.net.session_open  session_id={sess.session_id}  "
            f"client_id={sess.client_id or '?'}"
        )
        net_open_node = _RenderNode(label=net_open_label)
        cmd_node.children.append(net_open_node)

        # ---- envoy.stream under session_open ----
        # The envoy layer is optional — if the envoy debug-log parser
        # produced no streams (e.g. parser/regex didn't match this
        # envoy version), we still want the mongot.stream_open node
        # to appear below — it just hangs directly off session_open.
        parent_for_mongot = net_open_node
        if op.envoy_stream is not None:
            es = op.envoy_stream
            envoy_label = (
                f"envoy.stream  stream_id={es.stream_id}  "
                f"client_id={es.client_id or '?'}  path={es.path or '?'}"
            )
            envoy_node = _RenderNode(label=envoy_label)
            net_open_node.children.append(envoy_node)
            parent_for_mongot = envoy_node
        else:
            parent_for_mongot.children.append(
                _RenderNode(label="envoy.stream  (no match)")
            )

        # ---- mongot.stream_open under envoy.stream (or session_open) ----
        if op.mongot_stream_open is not None:
            mso = op.mongot_stream_open
            stream_id_label = (
                str(tree.mongot_stream_id)
                if tree is not None and tree.mongot_stream_id is not None
                else "?"
            )
            mso_label = (
                f"mongot.stream_open  pod={_short(mso.get('pod'))}  "
                f"streamId={stream_id_label}  "
                f"path={mso.get('path')}"
            )
            mso_node = _RenderNode(label=mso_label)
            parent_for_mongot.children.append(mso_node)
            # Batches arriving for the aggregate (the firstBatch)
            for b in op.mongot_batches:
                mso_node.children.append(
                    _RenderNode(label=f"mongot.batch  size={b.get('size')}")
                )
            if op.mongot_cmd is not None:
                mc = op.mongot_cmd
                mso_node.children.append(
                    _RenderNode(
                        label=f"mongot.cmd.{mc.get('command')}  cursorId={mc.get('cursor_id')}"
                    )
                )

    # ---- mongod.net.session_close (killCursors only) ----
    if op.mongod_session_close is not None:
        sess = op.mongod_session_close
        net_close_label = (
            f"mongod.net.session_close  session_id={sess.session_id}  "
            f"client_id={sess.client_id or '?'}  status={sess.status}"
        )
        net_close_node = _RenderNode(label=net_close_label)
        cmd_node.children.append(net_close_node)

        if op.mongot_stream_close is not None:
            mss = op.mongot_stream_close
            net_close_node.children.append(
                _RenderNode(
                    label=(
                        f"mongot.stream_close  streamId={mss.stream_id}  "
                        f"status={mss.grpc_status}  rst={mss.rst_stream}"
                    )
                )
            )

    # ---- mongot.batch directly under mongod.cmd.getMore (fresh pull) ----
    if op.command_name == "getMore" and op.mongot_batches:
        for b in op.mongot_batches:
            cmd_node.children.append(
                _RenderNode(label=f"mongot.batch  size={b.get('size')}")
            )

    return root


def _short(pod: Optional[str]) -> str:
    if pod is None:
        return "?"
    # File paths come in as ``/tmp/probe-stream-correlate/<pod>.log`` —
    # strip the dir + extension so the tree header reads cleanly.
    import os

    base = os.path.basename(pod)
    if base.endswith(".log"):
        base = base[:-4]
    return base


__all__ = [
    "StreamEvent",
    "StreamSummary",
    "MongodCommand",
    "MongodSession",
    "EnvoyStream",
    "ClientWireOp",
    "TimelineEvent",
    "CursorTree",
    "CursorTreeWireOp",
    "iter_log_lines",
    "parse_mongot_log_line",
    "parse_mongod_log_line",
    "parse_mongod_network_log_line",
    "parse_envoy_debug_log",
    "parse_client_wire_ops",
    "build_stream_summaries",
    "read_mongod_commands",
    "read_mongod_sessions",
    "read_mongot_interceptor_events",
    "correlate_sessions_with_cursors",
    "set_mongod_debug_logs",
    "unified_timeline",
    "build_cursor_trees",
    "print_stream_report",
    "print_mongod_command_report",
    "print_unified_timeline",
    "print_cursor_trees",
]
