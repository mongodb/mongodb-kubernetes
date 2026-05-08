"""mongot/mongod/envoy/pymongo debug-log analyzer.

Parses JSON DEBUG logs from mongot (Netty / gRPC + interceptor /
SearchCommand records), mongod (COMMAND / NETWORK), envoy
(http2/http debug), and in-process pymongo ``CommandListener`` events,
then aggregates them into a chronological cross-layer timeline keyed
on cursor / lsid / clientId UUID.

Join keys, no time tolerance:

  pymongo (lsid + server_connection_id)
     <-> mongod COMMAND      (attr.command.lsid + ctx=conn<N>)
     <-> mongod NETWORK      (clientId UUID, id=7401401 / 7401403)
     <-> envoy debug log     (mongodb-clientid request header)
     <-> mongot interceptor  (clientId on stream open, cursorId on commands)

When the mongot interceptor record is absent the analyzer falls back
to time-based correlation against the Netty stream summaries — see the
``parse_mongot_log_line`` / ``build_cursor_trees`` fallbacks.

Trigger sources:
- mongot: DEBUG on ``io.grpc.netty.NettyServerHandler`` +
  ``LuceneSearchBatchProducer`` + ``MongoDbGrpcProtocolInterceptor``
  (the dev mongot ships with these on).
- mongod: ``set_mongod_debug_logs()`` bumps COMMAND + NETWORK to 2.
- envoy: POST ``/logging?paths=http2:debug,http:debug,router:debug``
  on the admin endpoint.
- pymongo: ``SearchConnectivityTool`` installs a CommandListener at
  construction time.

See ``tmp/search-caching-investigation/observability-followup.md``.
"""

from __future__ import annotations

import json
import re
import threading
import time
from collections import defaultdict
from dataclasses import dataclass, field
from datetime import datetime, timezone
from typing import Any, Iterable, Optional, cast

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
    r"\[(?:Tags:\s*)?\"?ConnectionId\"?[:=]\"?(\d+)\"?" r"(?:,\s*\"?StreamId\"?[:=]\"?(\d+)\"?)?\]"
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
class MongosCommand:
    """One mongos command observed via COMMAND log records.

    Mongos slow-query records reuse the mongod COMMAND envelope. Fanout
    signals: ``attr.nShards`` / ``attr.numShards`` for the shard count,
    ``attr.cursorid`` for the top mongos cursor id (mongos exposes it as a
    flat scalar, NOT under ``cursor.id``). Per-shard sub-cursor topology
    is NOT surfaced in the slow-query record on 8.x mongos at command:2 —
    use ``MongosRemoteRequest`` (NETWORK id=4646300) for that.
    """

    timestamp: Optional[datetime]
    pod: str
    command: str  # "aggregate" | "getMore" | "killCursors" | ...
    namespace: Optional[str] = None
    cursor_id: Optional[int] = None  # top mongos cursor id (attr.cursorid)
    duration_ms: Optional[float] = None
    has_search_stage: bool = False
    num_shards: Optional[int] = None
    shards_targeted: list[str] = field(default_factory=list)
    lsid: Optional[str] = None
    server_connection_id: Optional[int] = None
    raw: dict[str, Any] = field(default_factory=dict)


@dataclass
class MongosRemoteRequest:
    """One mongos -> shard request observed via NETWORK id=4646300.

    Recovered from ``c=NETWORK, id=4646300, msg="Sending request"`` at
    network verbosity >= 2. ``target`` is ``<host>:<port>``; the host
    encodes the shard via its replica-set member naming convention
    (``<sh-resource>-<shardIndex>-<podIndex>``). ``server_connection_id``
    is the mongos ingress connection id (ctx=conn<N>) — joins this
    request back to the client aggregate/getMore that drove it.
    """

    timestamp: Optional[datetime]
    pod: str  # mongos pod name
    request_id: int
    target: str  # "<host>:<port>" of the destination mongod
    server_connection_id: Optional[int] = None


@dataclass
class MongodCommand:
    """One mongod command observed via the COMMAND DEBUG log.

    ``lsid`` is the hex form of ``attr.command.lsid.id``;
    ``server_connection_id`` is the integer parsed out of ``ctx=conn<N>``.
    Both are the join keys to pymongo's CommandListener events.
    """

    timestamp: Optional[datetime]
    pod: str
    command: str  # "aggregate" | "getMore" | "killCursors" | ...
    namespace: Optional[str] = None
    cursor_id: Optional[int] = None
    duration_ms: Optional[float] = None
    has_search_stage: bool = False
    lsid: Optional[str] = None
    server_connection_id: Optional[int] = None
    # Error / outcome fields from the COMMAND "Slow query" record's top-level
    # ``attr`` (id=51803). ``ok=0`` plus ``err_msg``/``err_name``/``err_code``
    # signal a server-side failure — for $search getMores, the canonical case
    # is mongod trying to refill from mongot and getting back e.g. "Socket
    # closed" (HostUnreachable).
    ok: Optional[int] = None
    err_msg: Optional[str] = None
    err_name: Optional[str] = None
    err_code: Optional[int] = None
    # ``attr.mongot`` carries the mongot-side cursor id + batchNum +
    # timeWaitingMillis when mongod actually issued a request to mongot for
    # this command. Its presence is the authoritative signal that this
    # getMore was a fresh mongot pull (versus served from mongod's local
    # batch buffer). ``None`` ⇒ served from buffer (no mongot call).
    mongot_request: Optional[dict] = None
    raw: dict[str, Any] = field(default_factory=dict)


@dataclass
class MongodSession:
    """One gRPC egress session on a mongod node, from NETWORK:2 records.

    Built from the paired ``LOGV2_DEBUG`` records ``id=7401401`` (open)
    and ``id=7401403`` (close). ``client_id`` is the UUID envoy logs as
    ``mongodb-clientid`` and the mongot interceptor logs on stream open.
    ``cursor_id`` is filled in by ``correlate_sessions_with_cursors``.
    """

    pod: str
    session_id: int
    client_id: str
    remote: Optional[str]
    opened_at: Optional[datetime]
    closed_at: Optional[datetime] = None
    status: Optional[str] = None  # gRPC final status (from 7401403)
    cursor_id: Optional[int] = None


@dataclass
class EnvoyStream:
    """One HTTP/2 stream observed on an envoy LB pod.

    Two complementary sources feed this struct:
      - runtime ``http2:debug``/``http:debug`` logs (frames + headers,
        gated behind ``--enable-envoy-debug``);
      - the always-on stdout JSON access log (one record per stream
        close — the only place ``upstream_host`` / ``response_flags`` /
        ``response_code`` surface).

    ``client_id`` is the ``mongodb-clientid`` header value — the join key
    out to mongod NETWORK records and the mongot interceptor.
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
    # Access-log fields (populated from EnvoyAccessLogEntry — the only
    # source for routing / response disposition; absent on debug-only
    # streams).
    upstream_host: Optional[str] = None
    response_code: Optional[int] = None
    response_flags: Optional[str] = None
    access_log_duration_ms: Optional[float] = None
    access_log_bytes_in: Optional[int] = None
    access_log_bytes_out: Optional[int] = None
    # Marks streams synthesised solely from an access-log entry (no
    # matching debug-mode runtime frames). Useful so the printer can
    # render "stream_id=?" for the wire stream id when only the
    # access-log surface is available.
    from_access_log_only: bool = False

    @property
    def lifetime_seconds(self) -> Optional[float]:
        if self.opened_at and self.closed_at:
            return (self.closed_at - self.opened_at).total_seconds()
        return None


@dataclass
class EnvoyAccessLogEntry:
    """One envoy stdout JSON access-log record (one per HTTP/2 stream close).

    Emitted by the always-on stdout access logger configured in
    ``buildHCMAccessLog`` (controllers/operator/envoy_config_builder.go).
    Unlike the runtime debug log, this surface does NOT require
    ``--enable-envoy-debug`` — every stream close lands here. Routing /
    upstream disposition lives ONLY on this record.
    """

    timestamp: Optional[datetime]
    pod: str
    client_id: Optional[str]
    upstream_host: Optional[str]
    request_path: Optional[str]
    response_code: Optional[int]
    grpc_status: Optional[str]
    response_flags: Optional[str]
    bytes_received: Optional[int]
    bytes_sent: Optional[int]
    duration_ms: Optional[float]
    raw: dict = field(default_factory=dict)


@dataclass
class ClientWireOp:
    """One wire-protocol command captured by the pymongo CommandListener.

    Analyzer-side mirror of ``connectivity.ClientWireOp``; accepts the
    in-process record OR a serialised replay. ``lsid`` and
    ``server_connection_id`` are the join keys to ``MongodCommand``.
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


class CommandEventListener:
    """Thread-safe pymongo ``CommandListener`` that accumulates wire-op events.

    Attach to a ``MongoClient`` via ``event_listeners=[listener]`` to capture
    all wire-protocol events for post-hoc analysis via the log analyzer.

    ``current_marker()`` → current event count (use as *since* snapshot anchor).
    ``snapshot_since(marker)`` → list of ``ClientWireOp`` recorded after *marker*.
    ``snapshot_since(0)`` → all events ever recorded.
    """

    def __init__(self) -> None:
        self._lock = threading.Lock()
        self._events: list[ClientWireOp] = []

    # pymongo CommandListener protocol ----------------------------------------

    def started(self, event: Any) -> None:
        self._record(event, "started")

    def succeeded(self, event: Any) -> None:
        self._record(event, "succeeded")

    def failed(self, event: Any) -> None:
        self._record(event, "failed")

    def _record(self, event: Any, phase: str) -> None:
        lsid_hex: Optional[str] = None
        try:
            lsid_raw = getattr(event, "command", {}).get("lsid") or {}
            if lsid_raw and "id" in lsid_raw:
                raw_id = lsid_raw["id"]
                lsid_hex = raw_id.hex() if hasattr(raw_id, "hex") else str(raw_id)
        except Exception:
            pass

        cursor_id: Optional[int] = None
        try:
            reply = getattr(event, "reply", {}) or {}
            cursor = reply.get("cursor") or {}
            cid = cursor.get("id")
            if cid is not None:
                cursor_id = int(cid)
        except Exception:
            pass

        n_returned: Optional[int] = None
        try:
            reply = getattr(event, "reply", {}) or {}
            cursor = reply.get("cursor") or {}
            batch = cursor.get("firstBatch") or cursor.get("nextBatch")
            if batch is not None:
                n_returned = len(batch)
        except Exception:
            pass

        duration_micros: Optional[int] = None
        try:
            dur = getattr(event, "duration_micros", None)
            if dur is not None:
                duration_micros = int(dur)
        except Exception:
            pass

        failure: Optional[str] = None
        try:
            raw_fail = getattr(event, "failure", None)
            if raw_fail is not None:
                failure = str(raw_fail)
        except Exception:
            pass

        op = ClientWireOp(
            phase=phase,
            command_name=getattr(event, "command_name", ""),
            request_id=getattr(event, "request_id", 0),
            timestamp=datetime.fromtimestamp(time.time(), tz=timezone.utc),
            server_connection_id=getattr(event, "server_connection_id", None),
            lsid=lsid_hex,
            cursor_id=cursor_id,
            duration_micros=duration_micros,
            n_returned=n_returned,
            database_name=getattr(event, "database_name", None),
            operation_id=getattr(event, "operation_id", None),
            failure=failure,
        )
        with self._lock:
            self._events.append(op)

    # Snapshot interface -------------------------------------------------------

    def current_marker(self) -> int:
        """Return current event count; pass as *since* to ``snapshot_since``."""
        with self._lock:
            return len(self._events)

    def snapshot_since(self, since: int) -> list[ClientWireOp]:
        """Return all events recorded after index *since* (0 = all events)."""
        with self._lock:
            return list(self._events[since:])


@dataclass
class CursorTreeWireOp:
    """One client wire op plus its matching lower-layer events.

    Started + succeeded/failed records (sharing ``request_id``) are
    collapsed into a single entry. Lower-layer fields are filled by
    ``build_cursor_trees`` via deterministic join keys with time-window
    fallbacks. Fields that don't apply to a given wire-op type stay None
    (e.g. ``envoy_stream`` only on aggregate, since the gRPC stream
    opens once per cursor).
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
    mongot_stream_open: Optional[dict] = None  # mongot interceptor record
    mongot_stream_close: Optional["StreamSummary"] = None
    mongot_batches: list[dict] = field(default_factory=list)
    mongot_cmd: Optional[dict] = None  # mongot SearchCommand/GetMore/KillCursors record

    # True iff at least one mongot ``batch_prepared`` event landed inside
    # this op's wire window (within _MONGOT_BATCH_MATCH_SLACK_SECONDS).
    served_fresh_from_mongot: bool = False


@dataclass
class CursorTree:
    """All events for one ``$search`` cursor, grouped under one root.

    Identified by the mongod-side ``cursor_id``. Tree-level keys:
    ``client_lsid``, ``mongod_pod``, ``mongot_pod``, ``client_id_uuid``
    (the cross-layer UUID — mongod NETWORK, envoy header, mongot
    interceptor), and ``mongot_stream_id`` (the Netty stream id).
    """

    cursor_id: int
    client_lsid: Optional[str] = None
    mongod_pod: Optional[str] = None
    mongot_pod: Optional[str] = None
    client_id_uuid: Optional[str] = None
    mongot_stream_id: Optional[int] = None
    wire_ops: list[CursorTreeWireOp] = field(default_factory=list)


@dataclass
class ShardedCursorBranch:
    """One per-shard branch of a sharded $search fanout.

    A sharded ``$search`` aggregate at mongos opens one sub-cursor per
    targeted shard. Each sub-cursor has its own mongod (the shard's
    primary), its own mongot (round-robin per shard's mongot replicas),
    its own gRPC clientId, and its own batch flow. This dataclass holds
    everything observed for one such branch.
    """

    shard_name: str  # e.g. "mdb-sh-conn-tool-0"
    mongod_pod: Optional[str] = None  # shard's primary mongod pod
    target_host: Optional[str] = None  # "<host>:<port>" from mongos NETWORK
    sub_cursor_id: Optional[int] = None  # cursor id mongod assigned for shard's portion
    mongot_pod: Optional[str] = None
    client_id_uuid: Optional[str] = None
    mongot_stream_id: Optional[int] = None
    # The shard's mongod COMMAND records (aggregate / getMore / killCursors)
    # observed for this sub-cursor — sorted by timestamp.
    mongod_commands: list["MongodCommand"] = field(default_factory=list)
    mongod_session: Optional["MongodSession"] = None
    envoy_stream: Optional["EnvoyStream"] = None
    mongot_stream_open: Optional[dict] = None
    mongot_stream_summary: Optional["StreamSummary"] = None
    mongot_batches: list[dict] = field(default_factory=list)


@dataclass
class ShardedCursorTree:
    """All events for one sharded ``$search`` cursor, grouped per shard.

    Top-level keys ``top_cursor_id``, ``client_lsid``, ``mongos_pod`` come
    from the mongos slow-query record. Each shard's events live under a
    ``ShardedCursorBranch`` in ``branches``.
    """

    top_cursor_id: int  # mongos-assigned top cursor id (attr.cursorid)
    client_lsid: Optional[str] = None
    mongos_pod: Optional[str] = None
    num_shards: Optional[int] = None  # mongos's attr.nShards
    branches: list[ShardedCursorBranch] = field(default_factory=list)
    # Client wire ops on this top cursor (aggregate / getMore / killCursors).
    wire_ops: list[CursorTreeWireOp] = field(default_factory=list)
    # Mongos's own COMMAND records on this top cursor — chronological.
    mongos_commands: list["MongosCommand"] = field(default_factory=list)


# Slack for matching a mongot batch_prepared event to a client wire op:
# ±50ms — more than the observed cross-process clock skew, less than the
# minimum inter-getMore interval used by paging_search.
_MONGOT_BATCH_MATCH_SLACK_SECONDS = 0.050


@dataclass
class TimelineEvent:
    """One event on the unified cross-layer timeline.

    ``layer`` is one of: ``client``, ``mongod.cmd``, ``mongod.net``,
    ``envoy``, ``mongot.frame``, ``mongot.batch``.
    """

    timestamp: datetime
    layer: str
    pod: Optional[str]
    kind: str  # layer-specific event kind
    client_id: Optional[str] = None  # UUID across mongod NETWORK / envoy / mongot
    lsid: Optional[str] = None
    cursor_id: Optional[int] = None
    server_connection_id: Optional[int] = None
    stream_id: Optional[int] = None  # envoy / mongot HTTP/2 stream id
    session_id: Optional[int] = None  # mongod NETWORK session id
    details: dict[str, Any] = field(default_factory=dict)


# ----------------------------------------------------------------------
# Log readers
# ----------------------------------------------------------------------


def iter_log_lines(paths: Iterable[str]) -> Iterable[tuple[str, str]]:
    """Yield ``(path, line)`` for every line in the given log files."""
    for path in paths:
        with open(path) as fh:
            for line in fh:
                yield (path, line.rstrip("\n"))


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
    if ts is None:
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
            # cursorId / clientId may be absent on older mongot builds;
            # _maybe_* returns None in that case.
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

    # --- MongoDbGrpcProtocolInterceptor — one DEBUG line per new gRPC
    # stream with clientId + grpc method. Absent on older mongot builds;
    # callers fall back to time-based envoy<->mongot correlation.
    if "MongoDbGrpcProtocolInterceptor" in name and "New mongot gRPC stream" in msg:
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

    # --- Mongot command TRACE logs (SearchCommand / GetMoreCommand /
    # KillCursorsCommand). On older mongot builds the ``cursorId`` key
    # is absent and this branch is skipped.
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
    """Parse one mongod log line, unwrapping the MCK launcher envelope."""
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
    """Return the lsid as dashless hex from a mongod COMMAND record's command doc.

    Accepts both extended-json ``$uuid`` and ``$binary`` (subType 04) shapes.
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
    """Parse one mongod JSON COMMAND line into a ``MongodCommand``.

    Handles both raw mongod JSON and the MCK launcher envelope.
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
        # Response cursor id lives at attr.cursorid (8.x flat form);
        # attr.command.cursor is the request shape (batchSize), not the reply.
        cursor_id = _maybe_int(attr.get("cursorid")) or _maybe_int(attr.get("cursorId"))
    elif cmd_name == "killCursors":
        ids = cmd_doc.get("cursors") or []
        cursor_id = ids[0] if ids else None
    pipeline = cmd_doc.get("pipeline") or []
    # ``$search`` on the client/mongos; rewritten to ``$search.mongotQuery``
    # on the shard side after fanout. Catch both shapes.
    has_search = any(
        isinstance(stage, dict)
        and ("$search" in stage or (isinstance(stage.get("$search"), dict) and "mongotQuery" in stage["$search"]))
        for stage in pipeline
    )
    mongot_request = attr.get("mongot") if isinstance(attr.get("mongot"), dict) else None
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
        ok=_maybe_int(attr.get("ok")),
        err_msg=_maybe_str(attr.get("errMsg")),
        err_name=_maybe_str(attr.get("errName")),
        err_code=_maybe_int(attr.get("errCode")),
        mongot_request=mongot_request,
        raw=cmd_doc,
    )


def parse_mongod_network_log_line(pod: str, line: str) -> Optional[tuple[str, dict]]:
    """Parse one mongod NETWORK log line into a SESSION_OPEN/CLOSE tuple.

    Recognised at verbosity 2: ``id=7401401`` (open) and ``id=7401403``
    (close). Returns ``None`` for any other record.
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
    """Return per-(pod, streamId) Netty summaries + a list of batch events.

    Interceptor records (``MONGOT_STREAM_OPEN`` / ``MONGOT_CMD``) belong
    to ``read_mongot_interceptor_events`` and are skipped here.
    """
    summaries: dict[tuple[str, int], StreamSummary] = {}
    batches: list[dict] = []
    for pod, line in iter_log_lines(log_sources):
        parsed = parse_mongot_log_line(pod, line)
        if parsed is None:
            continue
        if isinstance(parsed, tuple):
            # parse_mongot_log_line returns (str, dict) for tuple variants;
            # ty can't narrow a Union arm's element types through isinstance.
            tag = cast(str, parsed[0])
            payload = cast(dict, parsed[1])
            if tag == "BATCH":
                batches.append(payload)
            # MONGOT_STREAM_OPEN / MONGOT_CMD — captured by read_mongot_interceptor_events
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
    """Collect mongot interceptor records: ``(stream_opens, commands)``.

    Older mongot builds without the interceptor / cursorId key produce
    empty lists.
    """
    stream_opens: list[dict] = []
    commands: list[dict] = []
    for pod, line in iter_log_lines(log_sources):
        parsed = parse_mongot_log_line(pod, line)
        if not isinstance(parsed, tuple):
            continue
        kind = cast(str, parsed[0])
        payload = cast(dict, parsed[1])
        if kind == "MONGOT_STREAM_OPEN":
            stream_opens.append(payload)
        elif kind == "MONGOT_CMD":
            commands.append(payload)
    return stream_opens, commands


def read_mongod_commands(log_sources: Iterable[str], namespace: str = "ls-0") -> list[MongodCommand]:
    out: list[MongodCommand] = []
    for pod, line in iter_log_lines(log_sources):
        cmd = parse_mongod_log_line(pod, line)
        if cmd is not None:
            out.append(cmd)
    return out


def parse_mongos_log_line(pod: str, line: str) -> Optional[MongosCommand]:
    """Parse one mongos JSON COMMAND line into a ``MongosCommand``.

    Mongos reuses the mongod COMMAND envelope; fanout signals land in
    ``attr.nShards`` / ``attr.numShards`` and per-shard cursor lists under
    ``attr.cursor.cursors[].shardName`` / ``attr.shards[]``. All extras
    are optional — absent fields stay ``None`` / ``[]``.
    """
    rec = _unwrap_mongod_record(line)
    if rec is None or rec.get("c") != "COMMAND":
        return None
    attr = rec.get("attr") or {}
    cmd_doc = attr.get("command") or {}
    if not isinstance(cmd_doc, dict):
        return None
    cmd_name = None
    for k in cmd_doc:
        if not k.startswith("$") and not k.startswith("_"):
            cmd_name = k
            break
    if cmd_name not in {"aggregate", "getMore", "killCursors"}:
        return None
    ns = attr.get("ns") or attr.get("namespace")
    cursor_id: Optional[int] = None
    # 8.x mongos surfaces the top cursor id as flat ``attr.cursorid``
    # on the slow-query record (both aggregate and getMore replies).
    top_cursor = _maybe_int(attr.get("cursorid")) or _maybe_int(attr.get("cursorId"))
    if cmd_name == "getMore":
        cursor_id = _maybe_int(cmd_doc.get("getMore"))
    elif cmd_name == "aggregate":
        cursor_doc = cmd_doc.get("cursor") or {}
        if isinstance(cursor_doc, dict):
            cursor_id = _maybe_int(cursor_doc.get("id"))
    elif cmd_name == "killCursors":
        ids = cmd_doc.get("cursors") or []
        cursor_id = _maybe_int(ids[0]) if ids else None
    # Aggregate reply doesn't carry cursor.id on mongos (no firstBatch
    # echo of cursor.id); fall back to the flat ``attr.cursorid``.
    if cursor_id is None and top_cursor is not None:
        cursor_id = top_cursor
    pipeline = cmd_doc.get("pipeline") or []
    has_search = any(isinstance(stage, dict) and "$search" in stage for stage in pipeline)
    # Fanout signals — mongos surfaces these at various nesting depths
    # across server versions. Look in attr first, then under attr.cursor.
    num_shards = _maybe_int(attr.get("nShards"))
    if num_shards is None:
        num_shards = _maybe_int(attr.get("numShards"))
    shards_targeted: list[str] = []
    cursor_attr = attr.get("cursor")
    if isinstance(cursor_attr, dict):
        sub_cursors = cursor_attr.get("cursors")
        if isinstance(sub_cursors, list):
            for sc in sub_cursors:
                if isinstance(sc, dict):
                    sn = sc.get("shardName") or sc.get("shard")
                    if isinstance(sn, str):
                        shards_targeted.append(sn)
            if num_shards is None and sub_cursors:
                num_shards = len(sub_cursors)
    shards_attr = attr.get("shards")
    if isinstance(shards_attr, list):
        for s in shards_attr:
            if isinstance(s, str):
                shards_targeted.append(s)
            elif isinstance(s, dict):
                sn = s.get("shardName") or s.get("name")
                if isinstance(sn, str):
                    shards_targeted.append(sn)
        if num_shards is None:
            num_shards = len(shards_attr)
    return MongosCommand(
        timestamp=_mongod_record_timestamp(rec),
        pod=pod,
        command=cmd_name,
        namespace=ns,
        cursor_id=cursor_id,
        duration_ms=attr.get("durationMillis"),
        has_search_stage=has_search,
        num_shards=num_shards,
        shards_targeted=shards_targeted,
        lsid=_extract_lsid_hex(cmd_doc),
        server_connection_id=_extract_server_connection_id(rec.get("ctx")),
        raw=attr,
    )


def read_mongos_commands(log_sources: Iterable[str], namespace: str = "ls-0") -> list[MongosCommand]:
    """Walk per-mongos log sources, returning every parsed ``MongosCommand``."""
    out: list[MongosCommand] = []
    for pod, line in iter_log_lines(log_sources):
        cmd = parse_mongos_log_line(pod, line)
        if cmd is not None:
            out.append(cmd)
    return out


# mongos NETWORK id for "Sending request" — surfaces the per-shard fanout
# at network verbosity >= 2.
_MONGOS_LOG_ID_REMOTE_REQUEST = 4646300


def parse_mongos_remote_request_log_line(pod: str, line: str) -> Optional[MongosRemoteRequest]:
    """Parse one mongos NETWORK ``Sending request`` line.

    Used to recover per-shard fanout topology when the slow-query record
    doesn't surface ``cursor.cursors[]``. The remote request's
    ``ctx=conn<N>`` matches the client aggregate/getMore's
    ``server_connection_id``, joining the request back to the cursor.
    """
    rec = _unwrap_mongod_record(line)
    if rec is None or rec.get("c") != "NETWORK":
        return None
    if rec.get("id") != _MONGOS_LOG_ID_REMOTE_REQUEST:
        return None
    attr = rec.get("attr") or {}
    target = attr.get("target")
    req_id = _maybe_int(attr.get("requestId"))
    if not isinstance(target, str) or req_id is None:
        return None
    return MongosRemoteRequest(
        timestamp=_mongod_record_timestamp(rec),
        pod=pod,
        request_id=req_id,
        target=target,
        server_connection_id=_extract_server_connection_id(rec.get("ctx")),
    )


def read_mongos_remote_requests(log_sources: Iterable[str], namespace: str = "ls-0") -> list[MongosRemoteRequest]:
    """Walk per-mongos log sources for ``Sending request`` records."""
    out: list[MongosRemoteRequest] = []
    for pod, line in iter_log_lines(log_sources):
        req = parse_mongos_remote_request_log_line(pod, line)
        if req is not None:
            out.append(req)
    return out


def read_mongod_sessions(log_sources: Iterable[str], namespace: str = "ls-0") -> list[MongodSession]:
    """Build ``MongodSession`` objects from mongod NETWORK:2 log records."""
    by_key: dict[tuple[str, int], MongodSession] = {}
    for pod, line in iter_log_lines(log_sources):
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
                # CLOSE before OPEN — synthesise a stub session.
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
    """Fill ``MongodSession.cursor_id`` from matching COMMAND records.

    The session's cursor is the first ``$search`` aggregate inside its
    open/close window. Aggregate COMMAND records don't carry the
    response cursor id, so the fallback is the first matching-lsid
    getMore after the aggregate. Mutates ``sessions`` in place.
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
                    # Fall back to the first matching-lsid getMore after this aggregate.
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


def _envoy_line_message_and_ts(line: str) -> tuple[str, Optional[datetime]]:
    """Extract ``(message, timestamp)`` from one envoy component-log line.

    The operator configures envoy with ``--log-format`` so each line is a
    single JSON object ``{"time":..., "level":..., "message":...}``. Older
    captures (pre-JSON rollout) use the bracketed text format
    ``[2024-01-02 12:34:56.789][...] message...`` — those still flow
    through the existing regex-based timestamp parser and the raw line is
    returned verbatim as the "message".
    """
    stripped = line.strip()
    if stripped.startswith("{"):
        try:
            rec = json.loads(stripped)
        except json.JSONDecodeError:
            return line, _parse_envoy_timestamp(line)
        msg = rec.get("message")
        if not isinstance(msg, str):
            return line, _parse_envoy_timestamp(line)
        ts: Optional[datetime] = None
        ts_raw = rec.get("time")
        if isinstance(ts_raw, str):
            try:
                ts = datetime.fromisoformat(ts_raw.replace("Z", "+00:00"))
            except ValueError:
                ts = _parse_envoy_timestamp(msg)
        else:
            ts = _parse_envoy_timestamp(msg)
        return msg, ts
    return line, _parse_envoy_timestamp(line)


def parse_envoy_debug_log(log_sources: Iterable[str], namespace: str = "ls-0") -> list[EnvoyStream]:
    """Walk envoy ``http2:debug``/``http:debug`` output into per-stream summaries.

    The runtime debug log covers HTTP/2 FRAMES (headers, data, RST_STREAM,
    stream open/close). It is gated behind ``/logging?paths=...:debug`` —
    enable via ``log_analyzer_cli --enable-envoy-debug``. Routing /
    upstream disposition (``upstream_host``, ``response_code``,
    ``response_flags``, ``grpc_status``) live in the always-on STDOUT JSON
    access log instead — see ``parse_envoy_access_log_line`` /
    ``read_envoy_access_log``. The two surfaces are complementary, not
    duplicative; ``merge_envoy_access_log_into_streams`` joins them by
    ``client_id`` so the cursor tree's ``envoy.stream`` node carries both.

    The operator runs envoy with ``--log-format '{...}'`` (see
    ``mongodbsearchenvoy_controller.go``) so every component-log line is
    itself a JSON object; the per-frame body lives in ``message``. Older
    captures predating the JSON rollout (plain ``[ts][...] ...``) still
    parse correctly because ``_envoy_line_message_and_ts`` falls back to
    the legacy bracketed-text path.

    Post-hoc analyzer: enable per-frame envoy logging via the admin
    ``/logging`` endpoint before driving the workload, then feed the
    captured stdout into this function.
    """
    # envoy reuses (connection_id, stream_id) across pods — key on pod.
    streams_by_pod: dict[str, dict[tuple[int, int], EnvoyStream]] = defaultdict(dict)
    hcm_to_wire: dict[tuple[str, int, int], int] = {}
    # Track the active "request headers complete" connection so subsequent
    # indented header lines fold onto it.
    current_headers: dict[str, tuple[int, int]] = {}

    for pod, raw_line in iter_log_lines(log_sources):
        line, ts = _envoy_line_message_and_ts(raw_line)
        conn_m = _ENVOY_CONN_RE.search(line)

        # 1) "request headers complete" marks a new HCM stream.
        if conn_m and "request headers complete" in line:
            cid = int(conn_m.group(1))
            hcm_sid = int(conn_m.group(2)) if conn_m.group(2) else None
            if hcm_sid is None:
                continue
            current_headers[pod] = (cid, hcm_sid)
            continue

        # 2) Continuation header line under the active "headers complete".
        #    The wire stream_id is patched onto this entry by step 3.
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

        # 3) DATA frames carry the wire stream_id; promote the pending
        #    HCM-keyed entry on first sight.
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
                        promoted = EnvoyStream(pod=pod, connection_id=cid, stream_id=wire_sid)
                        streams_by_pod[pod][key] = promoted
                    stream = promoted
                if stream.opened_at is None:
                    stream.opened_at = ts
                # Direction is ambiguous from a single codec line;
                # everything is aggregated as outbound for byte/frame counts.
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
                m = re.search(r"stream (\d+)", line)
                if m:
                    wire_sid = int(m.group(1))
                    stream = streams_by_pod[pod].get((cid, wire_sid))
                    if stream is not None:
                        stream.rst_stream = True
                        stream.closed_at = ts

    # Drop HCM-only pending entries — no DATA frame ever arrived.
    out: list[EnvoyStream] = []
    for pod, by_key in streams_by_pod.items():
        for key, stream in by_key.items():
            if stream.stream_id < 0:
                continue
            out.append(stream)
    return out


def _parse_envoy_timestamp(line: str) -> Optional[datetime]:
    """Parse the leading ``[hh:mm:ss.mmm]`` envoy timestamp.

    Envoy debug logs are clock-only — today's UTC date is used as anchor.
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
    today = datetime.now(timezone.utc).date()
    try:
        return datetime.fromisoformat(f"{today.isoformat()}T{time_part}+00:00")
    except ValueError:
        return None


# ----------------------------------------------------------------------
# Envoy stdout JSON access-log parser
# ----------------------------------------------------------------------


def parse_envoy_access_log_line(pod: str, line: str) -> Optional[EnvoyAccessLogEntry]:
    """Parse one envoy stdout JSON access-log line.

    Returns ``None`` for non-JSON lines / records missing the load-bearing
    access-log fields (e.g. envoy's own startup banner, debug records).
    """
    line = line.strip()
    if not line or not line.startswith("{"):
        return None
    try:
        rec = json.loads(line)
    except json.JSONDecodeError:
        return None
    # Quick shape check: access-log records always carry these.
    # (Mongot's JSON DEBUG records share the prefix but use different keys.)
    if "response_code" not in rec and "response_flags" not in rec:
        return None

    # buildHCMAccessLog now emits a unified ``time`` field that matches
    # the runtime --log-format template. Fall back to the legacy ``ts``
    # field name so older captures still parse.
    ts_raw = rec.get("time") or rec.get("ts")
    ts: Optional[datetime] = None
    if isinstance(ts_raw, str):
        try:
            ts = datetime.fromisoformat(ts_raw.replace("Z", "+00:00"))
        except ValueError:
            ts = None

    def _i(v: Any) -> Optional[int]:
        if isinstance(v, bool):
            return None
        if isinstance(v, int):
            return v
        if isinstance(v, str) and v.isdigit():
            return int(v)
        return None

    def _f(v: Any) -> Optional[float]:
        if isinstance(v, (int, float)) and not isinstance(v, bool):
            return float(v)
        try:
            return float(v) if v is not None else None
        except (TypeError, ValueError):
            return None

    def _s(v: Any) -> Optional[str]:
        if v is None:
            return None
        return str(v) if v != "" else None

    return EnvoyAccessLogEntry(
        timestamp=ts,
        pod=pod,
        client_id=_s(rec.get("client_id")),
        upstream_host=_s(rec.get("upstream_host")),
        request_path=_s(rec.get("path")),
        response_code=_i(rec.get("response_code")),
        grpc_status=_s(rec.get("grpc_status")),
        response_flags=_s(rec.get("response_flags")),
        bytes_received=_i(rec.get("bytes_in")),
        bytes_sent=_i(rec.get("bytes_out")),
        duration_ms=_f(rec.get("duration_ms")),
        raw=rec,
    )


def read_envoy_access_log(log_sources: Iterable[str], namespace: str = "ls-0") -> list[EnvoyAccessLogEntry]:
    """Parse envoy stdout into a flat list of access-log entries.

    Walks the same ``kubectl logs``-style log sources as
    ``parse_envoy_debug_log`` — the access log records are interleaved in
    the same stream. Lines that aren't access-log JSON are silently
    skipped.
    """
    out: list[EnvoyAccessLogEntry] = []
    for pod, line in iter_log_lines(log_sources):
        entry = parse_envoy_access_log_line(pod, line)
        if entry is not None:
            out.append(entry)
    return out


def merge_envoy_access_log_into_streams(
    streams: list[EnvoyStream],
    access_log: list[EnvoyAccessLogEntry],
) -> list[EnvoyStream]:
    """Join access-log entries onto runtime debug-mode streams by ``client_id``.

    For every access-log entry:
      - if a debug-mode ``EnvoyStream`` exists for the same ``client_id``,
        populate its access-log fields (``upstream_host`` etc) in place;
      - otherwise synthesise a new ``EnvoyStream`` marked
        ``from_access_log_only=True``. The synthesised stream carries
        ``stream_id=-1`` (the codec wire id is unknowable without the
        debug log).

    Returns the merged list.
    """
    by_client_id: dict[str, EnvoyStream] = {}
    for es in streams:
        if es.client_id and es.client_id not in by_client_id:
            by_client_id[es.client_id] = es
    out = list(streams)
    for entry in access_log:
        if not entry.client_id:
            continue
        existing = by_client_id.get(entry.client_id)
        if existing is not None:
            # Merge access-log fields onto the debug-mode stream.
            existing.upstream_host = existing.upstream_host or entry.upstream_host
            existing.response_code = (
                existing.response_code if existing.response_code is not None else entry.response_code
            )
            existing.response_flags = existing.response_flags or entry.response_flags
            existing.access_log_duration_ms = (
                existing.access_log_duration_ms if existing.access_log_duration_ms is not None else entry.duration_ms
            )
            existing.access_log_bytes_in = (
                existing.access_log_bytes_in if existing.access_log_bytes_in is not None else entry.bytes_received
            )
            existing.access_log_bytes_out = (
                existing.access_log_bytes_out if existing.access_log_bytes_out is not None else entry.bytes_sent
            )
            # grpc_status from access log is "OK"/"Unavailable" etc;
            # debug log carries the numeric "0"/"14". Keep both when
            # available, preferring the access-log string when only one
            # is present (it's more reader-friendly).
            if not existing.grpc_status and entry.grpc_status:
                existing.grpc_status = entry.grpc_status
            if entry.timestamp is not None and existing.closed_at is None:
                existing.closed_at = entry.timestamp
            if entry.request_path and not existing.path:
                existing.path = entry.request_path
        else:
            synth = EnvoyStream(
                pod=entry.pod,
                connection_id=-1,
                stream_id=-1,
                client_id=entry.client_id,
                path=entry.request_path,
                opened_at=None,
                closed_at=entry.timestamp,
                grpc_status=entry.grpc_status,
                upstream_host=entry.upstream_host,
                response_code=entry.response_code,
                response_flags=entry.response_flags,
                access_log_duration_ms=entry.duration_ms,
                access_log_bytes_in=entry.bytes_received,
                access_log_bytes_out=entry.bytes_sent,
                from_access_log_only=True,
            )
            by_client_id[entry.client_id] = synth
            out.append(synth)
    return out


# ----------------------------------------------------------------------
# pymongo CommandListener event parser
# ----------------------------------------------------------------------


def parse_client_wire_ops(events: list[Any], *, anchor_wall_time: Optional[datetime] = None) -> list[ClientWireOp]:
    """Normalise CommandListener records (dataclass or dict) into ``ClientWireOp``.

    ``anchor_wall_time`` anchors ``time.monotonic`` deltas to wall-clock
    so the timeline can interleave them with log-parsed events. Without
    it, monotonic values become epoch-relative datetimes.
    """
    # Anchor every record to anchor_wall_time + (ts - min_ts).
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
                ts = datetime.fromtimestamp(float(ts_raw), tz=timezone.utc)
            except (OverflowError, OSError, ValueError):
                ts = datetime(1970, 1, 1, tzinfo=timezone.utc)
        else:
            ts = datetime(1970, 1, 1, tzinfo=timezone.utc)
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
# Sharded cursor-tree view
# ----------------------------------------------------------------------


def _shard_from_target(target: str, shard_pod_prefixes: dict[str, str]) -> Optional[str]:
    """Match a host / pod / log-file path to its shard name.

    Accepts:
      - ``<pod>:<port>`` (mongos NETWORK target);
      - ``<pod>.<svc>.<ns>.svc...:port`` (FQDN target);
      - ``/tmp/.../<pod>.log`` (post-``fetch_pod_logs`` log file).

    ``shard_pod_prefixes`` maps shard name -> the pod-name prefix
    (e.g. ``"mdb-sh-conn-tool-0" -> "mdb-sh-conn-tool-0-"``).
    """
    import os

    name = target
    # Log-file path: keep the basename without the ``.log`` suffix.
    if "/" in name:
        name = os.path.basename(name)
        if name.endswith(".log"):
            name = name[:-4]
    # ``<host>:<port>``  ->  ``<host>``
    name = name.split(":")[0]
    # FQDN ``<pod>.<svc>...``  ->  ``<pod>``
    name = name.split(".")[0]
    for shard_name, prefix in shard_pod_prefixes.items():
        if name.startswith(prefix):
            return shard_name
    return None


def render_sharded_cursor_trees(trees: list[ShardedCursorTree]) -> str:
    """Same rendering as ``print_sharded_cursor_trees`` but returns a string."""
    import contextlib
    import io

    buf = io.StringIO()
    with contextlib.redirect_stdout(buf):
        print_sharded_cursor_trees(trees)
    return buf.getvalue()


def print_sharded_cursor_trees(trees: list[ShardedCursorTree]) -> None:
    """Render per-cursor sharded fanout trees."""
    print(f"\n=== per-sharded-cursor tree view — {len(trees)} cursor(s) ===")
    for tree in trees:
        header = [
            f"cursor {tree.top_cursor_id}",
            f"lsid={tree.client_lsid or '?'}",
            f"mongos={_short(tree.mongos_pod)}",
            f"num_shards={tree.num_shards}",
            f"branches={len(tree.branches)}",
            f"wire_ops={len(tree.wire_ops)}",
        ]
        print()
        print("  " + "  ".join(header))
        # Mongos's own command timeline
        mc_lines = []
        for mc in tree.mongos_commands[:8]:
            ts = mc.timestamp.isoformat() if mc.timestamp else "?"
            mc_lines.append(f"mongos.{mc.command}  ts={ts}  dur_ms={mc.duration_ms}  nShards={mc.num_shards}")
        for line in mc_lines:
            print(f"    {line}")
        n = len(tree.branches)
        for i, br in enumerate(tree.branches):
            last = i == n - 1
            elbow = "└─" if last else "├─"
            cont = "    " if last else "│   "
            label = (
                f"shard {br.shard_name}  sub_cursor={br.sub_cursor_id}  "
                f"mongod={_short(br.mongod_pod)}  mongot={_short(br.mongot_pod)}  "
                f"clientId={br.client_id_uuid}  streamId={br.mongot_stream_id}"
            )
            print(f"  {elbow} {label}")
            # Per-branch event list
            cmds_shown = 0
            for cmd in br.mongod_commands:
                ts = cmd.timestamp.isoformat() if cmd.timestamp else "?"
                print(
                    f"  {cont}├─ mongod.{cmd.command}  ts={ts}  " f"cursor_id={cmd.cursor_id}  dur_ms={cmd.duration_ms}"
                )
                cmds_shown += 1
                if cmds_shown >= 5:
                    print(f"  {cont}├─ … ({len(br.mongod_commands) - cmds_shown} more)")
                    break
            if br.mongod_session is not None:
                sess = br.mongod_session
                print(
                    f"  {cont}├─ mongod.net.session_open  session_id={sess.session_id}  "
                    f"client_id={sess.client_id or '?'}"
                )
            if br.envoy_stream is not None:
                print(f"  {cont}├─ {_envoy_stream_label(br.envoy_stream)}")
            else:
                print(f"  {cont}├─ envoy.stream  (no match)")
            if br.mongot_stream_open is not None:
                mso = br.mongot_stream_open
                print(
                    f"  {cont}├─ mongot.stream_open  pod={_short(mso.get('pod'))}  "
                    f"streamId={br.mongot_stream_id}  path={mso.get('path')}"
                )
            else:
                print(f"  {cont}├─ mongot.stream_open  (no match)")
            for b in br.mongot_batches[:4]:
                print(f"  {cont}├─ mongot.batch  size={b.get('size')}")
            if len(br.mongot_batches) > 4:
                print(f"  {cont}└─ … ({len(br.mongot_batches) - 4} more batches)")
            else:
                print(f"  {cont}└─")


def _first_wire_op_time(tree: CursorTree) -> Optional[datetime]:
    for op in tree.wire_ops:
        if op.client_started is not None:
            return op.client_started
        if op.client_succeeded is not None:
            return op.client_succeeded
    return None


# ----------------------------------------------------------------------
# Mongod log-level helper
# ----------------------------------------------------------------------


def set_mongod_debug_logs(mongo_client, *, command_level: int = 2, network_level: int = 2) -> dict:
    """Bump mongod COMMAND/NETWORK verbosity; pair with the inverse to restore.

    COMMAND:2 surfaces ``$search`` aggregate/getMore wire-level records.
    NETWORK:2 surfaces the ``LOGV2_DEBUG(7401401/7401403)`` egress session
    pair carrying the ``clientId`` UUID — the cross-side join key. Without
    NETWORK:2 the mongod<->envoy<->mongot join falls back to time correlation.
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


def set_mongos_debug_logs(mongo_client, *, command_level: int = 2, network_level: int = 2) -> dict:
    """Bump mongos COMMAND/NETWORK verbosity; pair with the inverse to restore.

    Mongos surfaces ``$search`` aggregate/getMore slow-query records at
    COMMAND:2 with ``attr.cursorid`` (top mongos cursor id), ``attr.nShards``
    and lsid — the join surface to per-shard mongod records. NETWORK:2
    additionally surfaces ``id=4646300`` "Sending request" records with
    the destination host:port — used to recover per-shard fanout topology
    when the slow-query record doesn't carry ``cursor.cursors[]``.
    """
    res = mongo_client.admin.command(
        "setParameter",
        1,
        logComponentVerbosity={
            "command": {"verbosity": command_level},
            "network": {"verbosity": network_level},
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
    print(
        f"\n=== mongot HTTP/2 stream report — {len(summaries)} stream(s) across {len({k[0] for k in summaries})} pod(s) ==="
    )
    by_pod: dict[str, list[StreamSummary]] = defaultdict(list)
    for (pod, _sid), s in summaries.items():
        by_pod[pod].append(s)
    for pod, streams in by_pod.items():
        print(f"\n  pod={_short(pod)}")
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
            print(f"  pod={_short(pod)}  batches={len(bs)}  sizes=[{sizes}]  total={total}")


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
            f"  cursor_id={cid}  pod={_short(cs[0].pod)}  events={len(cs)}  "
            f"kinds={kinds[:8]}{'...' if len(kinds) > 8 else ''}  span={first} → {last}"
        )


# Disjoint ANSI palettes so no cursor_id ever shares a color with an lsid.
_CURSOR_COLORS = [31, 32, 33, 91, 92, 93]  # reds / greens / yellows
_LSID_COLORS = [34, 35, 36, 94, 95, 96]  # blues / magentas / cyans
# Back-compat alias for print_lsid_timeline's default palette.
_LSID_PALETTE = _CURSOR_COLORS


def _ansi_wrap(code: int, text: str) -> str:
    return f"\033[{code}m{text}\033[0m"


def _color_for(value: Any, palette: list[int]) -> int:
    """Stable hash of ``value`` into ``palette`` (None -> palette[0])."""
    if value is None:
        return palette[0]
    return palette[hash(("p", value)) % len(palette)]


# Back-compat: prior callers pass cursor_id positionally.
def _color_for_cursor(cursor_id: Optional[int], palette: list[int]) -> int:
    return _color_for(cursor_id, palette)


def print_unified_timeline(
    events: list[TimelineEvent],
    *,
    max_events: int = 200,
    color: Optional[bool] = None,
) -> None:
    """Print events interleaved by timestamp with join keys per line.

    ``cursor_id`` and ``lsid`` are color-coded with disjoint palettes so
    the two key spaces never share a color. ``max_events=0`` prints every
    event.
    """
    use_color = _ansi_color_enabled(color)
    print(f"\n=== unified cross-layer timeline — {len(events)} event(s) ===")
    if not events:
        return
    head = events if max_events <= 0 else events[:max_events]
    for ev in head:
        ts = ev.timestamp.isoformat() if ev.timestamp else "?"
        pieces = [ts, f"{ev.layer:<13}", f"pod={_short(ev.pod) if ev.pod else '-'}", f"kind={ev.kind}"]
        for label, value, palette in (
            ("client_id", ev.client_id, None),
            ("lsid", ev.lsid, _LSID_COLORS),
            ("cursor_id", ev.cursor_id, _CURSOR_COLORS),
            ("conn_id", ev.server_connection_id, None),
            ("stream_id", ev.stream_id, None),
            ("session_id", ev.session_id, None),
        ):
            if value is None:
                continue
            text = f"{label}={value}"
            if use_color and palette is not None:
                text = _ansi_wrap(_color_for(value, palette), text)
            pieces.append(text)
        if ev.details:
            extras = " ".join(f"{k}={v}" for k, v in ev.details.items() if v is not None)
            if extras:
                pieces.append(extras)
        print("  " + "  ".join(pieces))
    if max_events and len(events) > max_events:
        print(f"  ... ({len(events) - max_events} more event(s) elided)")


def _ansi_color_enabled(force: Optional[bool]) -> bool:
    """Resolve final color toggle from ``--color`` / ``--no-color`` / env / TTY."""
    if force is True:
        return True
    if force is False:
        return False
    # Default: TTY + NO_COLOR unset.
    import os
    import sys

    if os.environ.get("NO_COLOR") is not None:
        return False
    try:
        return sys.stdout.isatty()
    except (AttributeError, ValueError):
        return False


def _short_cursor_tag(cursor_id: Optional[int]) -> str:
    """Short stable tag derived from cursor_id — used as no-color fallback."""
    if cursor_id is None:
        return "[c?    ]"
    s = str(cursor_id)
    if len(s) <= 6:
        return f"[c{s:<6}]"
    # Keep first 3 + last 3 digits for visual uniqueness.
    return f"[c{s[:3]}…{s[-3:]}]"


def collect_lsids_in_window(events: list[TimelineEvent]) -> dict[str, dict[str, Any]]:
    """Walk the timeline once, recover one summary entry per distinct lsid.

    Per-lsid keys: ``cursor_ids`` (set), ``client_ids`` (set), ``pods`` (set),
    ``num_events`` (int), ``first_ts`` / ``last_ts`` (datetime).
    """
    out: dict[str, dict[str, Any]] = {}
    for ev in events:
        if not ev.lsid:
            continue
        entry = out.setdefault(
            ev.lsid,
            {
                "cursor_ids": set(),
                "client_ids": set(),
                "pods": set(),
                "num_events": 0,
                "first_ts": ev.timestamp,
                "last_ts": ev.timestamp,
            },
        )
        if ev.cursor_id is not None:
            entry["cursor_ids"].add(ev.cursor_id)
        if ev.client_id:
            entry["client_ids"].add(ev.client_id)
        if ev.pod:
            entry["pods"].add(ev.pod)
        entry["num_events"] += 1
        if ev.timestamp < entry["first_ts"]:
            entry["first_ts"] = ev.timestamp
        if ev.timestamp > entry["last_ts"]:
            entry["last_ts"] = ev.timestamp
    return out


def summarize_lsids_in_window(events: list[TimelineEvent]) -> list[dict[str, Any]]:
    """List of per-lsid summaries sorted by first_ts. Each dict carries
    ``lsid``, ``cursor_id`` (first non-None), ``num_shards`` (None for RS;
    derived from mongos.cmd events when present), ``mongos_pod`` (when
    detected), ``num_events``, ``first_ts``, ``last_ts``."""
    raw = collect_lsids_in_window(events)
    # Pick a primary cursor_id + mongos_pod per lsid.
    cursor_per_lsid: dict[str, Optional[int]] = {}
    mongos_pod_per_lsid: dict[str, Optional[str]] = {}
    num_shards_per_lsid: dict[str, Optional[int]] = {}
    for ev in events:
        if not ev.lsid:
            continue
        if ev.cursor_id is not None and cursor_per_lsid.get(ev.lsid) is None:
            cursor_per_lsid[ev.lsid] = ev.cursor_id
        if ev.layer == "mongos.cmd":
            if mongos_pod_per_lsid.get(ev.lsid) is None and ev.pod:
                mongos_pod_per_lsid[ev.lsid] = ev.pod
            ns = ev.details.get("num_shards") if ev.details else None
            if num_shards_per_lsid.get(ev.lsid) is None and ns is not None:
                num_shards_per_lsid[ev.lsid] = ns
    out: list[dict[str, Any]] = []
    for lsid, entry in raw.items():
        out.append(
            {
                "lsid": lsid,
                "cursor_id": cursor_per_lsid.get(lsid),
                "num_shards": num_shards_per_lsid.get(lsid),
                "mongos_pod": mongos_pod_per_lsid.get(lsid),
                "num_events": entry["num_events"],
                "first_ts": entry["first_ts"],
                "last_ts": entry["last_ts"],
                "cursor_ids": sorted(entry["cursor_ids"]),
                "client_ids": sorted(entry["client_ids"]),
            }
        )
    out.sort(key=lambda d: d["first_ts"])
    return out


def print_lsids_summary(events: list[TimelineEvent]) -> None:
    """Single-line-per-lsid header printed at the top of normal output.

    Useful so the user knows which ``--lsid <hex>`` to feed back in on a
    follow-up filtered run.
    """
    summary = summarize_lsids_in_window(events)
    if not summary:
        return
    print(f"\n=== detected lsids in window — {len(summary)} ===")
    for s in summary:
        mongos = _short(s["mongos_pod"]) if s["mongos_pod"] else "-"
        ns = s["num_shards"] if s["num_shards"] is not None else "-"
        cursor = s["cursor_id"] if s["cursor_id"] is not None else "?"
        print(f"  {s['lsid']}  cursor_id={cursor}  mongos={mongos}  " f"num_shards={ns}  events={s['num_events']}")


def _events_for_lsid(events: list[TimelineEvent], lsid: str) -> list[TimelineEvent]:
    """Filter events that 'belong to' ``lsid``.

    Belongs-to lineage (fixed-point closure):
      1. ev.lsid == lsid                                   seeds cursor/client sets;
      2. ev.cursor_id in cursor_set                         pulls in mongod NETWORK / envoy / mongot events;
      3. ev.client_id in client_set                         pulls in envoy access-log entries lacking cursor_id;
      4. closure: events matched via (2) or (3) re-seed cursor/client sets,
         so per-shard sub-cursors and their downstream events get included too.

    Step 4 is required because the inbound chain is layered: mongos
    aggregates carry the lsid; per-shard mongod sub-cursor events carry
    a different cursor_id but join via mongos NETWORK; envoy/mongot
    events carry only client_id, joined back via mongod NETWORK
    session_open which carries (cursor_id, client_id) but no lsid.
    """
    cursor_set: set[int] = set()
    client_set: set[str] = set()
    for ev in events:
        if ev.lsid == lsid:
            if ev.cursor_id is not None:
                cursor_set.add(ev.cursor_id)
            if ev.client_id:
                client_set.add(ev.client_id)

    # Iterate to fixed point — bounded by O(layers) so 3-4 passes suffice.
    for _ in range(8):
        grew = False
        for ev in events:
            seed = ev.lsid == lsid
            if not seed and ev.cursor_id is not None and ev.cursor_id in cursor_set:
                seed = True
            if not seed and ev.client_id and ev.client_id in client_set:
                seed = True
            if not seed:
                continue
            if ev.cursor_id is not None and ev.cursor_id not in cursor_set:
                cursor_set.add(ev.cursor_id)
                grew = True
            if ev.client_id and ev.client_id not in client_set:
                client_set.add(ev.client_id)
                grew = True
        if not grew:
            break

    out: list[TimelineEvent] = []
    for ev in events:
        if ev.lsid == lsid:
            out.append(ev)
        elif ev.cursor_id is not None and ev.cursor_id in cursor_set:
            out.append(ev)
        elif ev.client_id and ev.client_id in client_set:
            out.append(ev)
    return out


def print_lsid_timeline(
    events: list[TimelineEvent],
    lsid: str,
    *,
    color: Optional[bool] = None,
    palette: Optional[list[int]] = None,
) -> None:
    """Chronological interleaved per-lsid timeline, color-coded by cursor_id.

    Lines are sorted by timestamp. Each line carries ``cursor_id`` and an
    lsid suffix. Color resolution:

      - ``color=True``  -> ANSI escapes;
      - ``color=False`` -> short stable [c<...>] tag prefix;
      - ``color=None``  -> auto: TTY + NO_COLOR unset.
    """
    filtered = _events_for_lsid(events, lsid)
    if not filtered:
        print(f"\n# no events for lsid {lsid} in this window")
        return

    use_color = _ansi_color_enabled(color)
    pal = palette or _LSID_PALETTE
    lsid_suffix = lsid[:8]

    print(f"\n=== single-lsid interleaved timeline — lsid={lsid}  " f"events={len(filtered)} ===")
    for ev in filtered:
        ts = ev.timestamp.isoformat() if ev.timestamp else "?"
        cursor = ev.cursor_id
        cursor_label = str(cursor) if cursor is not None else "?"
        pieces = [
            ts,
            f"{ev.layer:<13}",
            f"pod={_short(ev.pod) if ev.pod else '-'}",
            f"kind={ev.kind}",
            f"cursor_id={cursor_label}",
            f"lsid={lsid_suffix}",
        ]
        if ev.client_id:
            pieces.append(f"client_id={ev.client_id[:8]}")
        if ev.details:
            extras = " ".join(f"{k}={v}" for k, v in ev.details.items() if v is not None)
            if extras:
                pieces.append(extras)

        body = "  ".join(pieces)
        if use_color:
            code = _color_for_cursor(cursor, pal)
            print(f"\x1b[{code}m{body}\x1b[0m")
        else:
            print(f"{_short_cursor_tag(cursor)}  {body}")


# Internal node for the recursive box-drawing renderer.
@dataclass
class _RenderNode:
    label: str
    children: list["_RenderNode"] = field(default_factory=list)


def print_cursor_trees(trees: list[CursorTree]) -> None:
    """Render per-cursor box-drawing trees: client.cmd → mongod.cmd →
    mongod.net.session → envoy.stream → mongot.stream/batch/cmd."""
    print(f"\n=== per-cursor tree view — {len(trees)} cursor(s) ===")
    if not trees:
        return

    for tree in trees:
        anchor = _first_wire_op_time(tree)
        header_bits = [f"cursor {tree.cursor_id}"]
        header_bits.append(f"lsid={tree.client_lsid or '?'}")
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
    """Build the box-drawing tree for one wire op (``tree`` for streamId label)."""
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
        # Outcome classification using mongod's own COMMAND attrs:
        #   ``attr.ok == 0`` + ``errMsg`` mentioning mongot ⇒ mongod tried to
        #       pull from mongot and the call failed (e.g. envoy dead → Socket
        #       closed → HostUnreachable). Tag as failed even if timeWaitingMs
        #       is 0 (immediate connection error never waits).
        #   ``attr.mongot.timeWaitingMillis > 0`` ⇒ mongod actually issued a
        #       network call to mongot and waited for its response.
        #   ``attr.mongot`` present with timeWaitingMillis == 0 ⇒ served from
        #       mongod's local batch buffer (the mongot field still names the
        #       source batch — that's how it's logged — but no network call).
        # Falls back to ``served_fresh_from_mongot`` (batch-window heuristic)
        # when the mongod cmd wasn't captured.
        cmd = op.mongod_cmd
        if cmd is not None and cmd.ok == 0 and (cmd.err_msg or cmd.err_name):
            err = cmd.err_name or "error"
            tag = f"   ← FAILED MONGOT PULL  {err}"
        elif cmd is not None and cmd.mongot_request and (cmd.mongot_request.get("timeWaitingMillis") or 0) > 0:
            sizes = ",".join(str(b.get("size")) for b in op.mongot_batches)
            wait_ms = cmd.mongot_request.get("timeWaitingMillis")
            batch_num = cmd.mongot_request.get("batchNum")
            tag = f"   ← FRESH MONGOT PULL (batchNum={batch_num} waited={wait_ms}ms"
            if sizes:
                tag += f" batch={sizes}"
            tag += ")"
        elif op.served_fresh_from_mongot:
            sizes = ",".join(str(b.get("size")) for b in op.mongot_batches)
            tag = f"   ← FRESH MONGOT PULL (batch={sizes})"
        else:
            tag = "   (cached — mongod buffer)"
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
        # No COMMAND match — render a leaf so the layer is visible.
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
    if cmd.ok == 0:
        cmd_pieces.append("ok=0")
        if cmd.err_code is not None:
            cmd_pieces.append(f"errCode={cmd.err_code}")
        if cmd.err_name:
            cmd_pieces.append(f"errName={cmd.err_name}")
        if cmd.err_msg:
            cmd_pieces.append(f"errMsg='{cmd.err_msg}'")
    cmd_node = _RenderNode(label="  ".join(cmd_pieces))
    root.children.append(cmd_node)

    # ---- mongod→mongot request (getMore only; attr.mongot in COMMAND log) ----
    # ``attr.mongot`` is logged on every $search getMore (it names the source
    # batch even on buffer hits). Only render the child for failures and real
    # mongot calls (``timeWaitingMillis > 0``) so the tree stays clean on
    # buffer-hit getMores, which are the common case.
    if op.command_name == "getMore" and cmd.mongot_request:
        mr = cmd.mongot_request
        wait_ms = mr.get("timeWaitingMillis") or 0
        if cmd.ok == 0 or wait_ms > 0:
            mr_pieces = ["mongod→mongot.request"]
            if mr.get("cursorid") is not None:
                mr_pieces.append(f"mongot_cursor_id={mr.get('cursorid')}")
            if mr.get("batchNum") is not None:
                mr_pieces.append(f"batchNum={mr.get('batchNum')}")
            mr_pieces.append(f"timeWaitingMs={wait_ms}")
            if cmd.ok == 0:
                mr_pieces.append("(FAILED)")
            cmd_node.children.append(_RenderNode(label="  ".join(mr_pieces)))

    # ---- mongod.net.session_open (aggregate only) ----
    if op.mongod_session_open is not None:
        sess = op.mongod_session_open
        net_open_label = f"mongod.net.session_open  session_id={sess.session_id}  " f"client_id={sess.client_id or '?'}"
        net_open_node = _RenderNode(label=net_open_label)
        cmd_node.children.append(net_open_node)

        # envoy.stream is optional; mongot.stream_open hangs off
        # session_open directly when there's no envoy match.
        parent_for_mongot = net_open_node
        if op.envoy_stream is not None:
            es = op.envoy_stream
            envoy_label = _envoy_stream_label(es)
            envoy_node = _RenderNode(label=envoy_label)
            net_open_node.children.append(envoy_node)
            parent_for_mongot = envoy_node
        else:
            parent_for_mongot.children.append(_RenderNode(label="envoy.stream  (no match)"))

        # ---- mongot.stream_open under envoy.stream (or session_open) ----
        if op.mongot_stream_open is not None:
            mso = op.mongot_stream_open
            stream_id_label = (
                str(tree.mongot_stream_id) if tree is not None and tree.mongot_stream_id is not None else "?"
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
                mso_node.children.append(_RenderNode(label=f"mongot.batch  size={b.get('size')}"))
            if op.mongot_cmd is not None:
                mc = op.mongot_cmd
                mso_node.children.append(
                    _RenderNode(label=f"mongot.cmd.{mc.get('command')}  cursorId={mc.get('cursor_id')}")
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
            cmd_node.children.append(_RenderNode(label=f"mongot.batch  size={b.get('size')}"))

    return root


def _envoy_stream_label(es: "EnvoyStream") -> str:
    """Render an envoy.stream tree line — debug-mode frames + access-log routing."""
    pieces = ["envoy.stream"]
    sid = es.stream_id if es.stream_id is not None and es.stream_id >= 0 else "?"
    pieces.append(f"stream_id={sid}")
    pieces.append(f"client_id={es.client_id or '?'}")
    if es.upstream_host:
        pieces.append(f"upstream={es.upstream_host}")
    if es.response_code is not None:
        pieces.append(f"resp={es.response_code}")
    if es.grpc_status:
        pieces.append(f"grpc={es.grpc_status}")
    if es.response_flags:
        pieces.append(f"flags={es.response_flags}")
    if es.path:
        pieces.append(f"path={es.path}")
    return "  ".join(pieces)


def _short(pod: Optional[str]) -> str:
    if pod is None:
        return "?"
    # Strip ``/tmp/.../<pod>.log`` -> ``<pod>`` for clean rendering.
    import os

    base = os.path.basename(pod)
    if base.endswith(".log"):
        base = base[:-4]
    return base


def summarize_cursor_pod_distribution(trees: list[CursorTree]) -> dict[str, int]:
    """Return mongot_pod -> count for trees that successfully resolved a mongot_pod."""
    out: dict[str, int] = {}
    for tree in trees:
        pod = tree.mongot_pod
        if not pod:
            continue
        out[pod] = out.get(pod, 0) + 1
    return out


def format_cursor_pod_distribution(distribution: dict[str, int]) -> str:
    """One-line table of pod -> cursor count."""
    if not distribution:
        return "(no cursor→pod resolutions)"
    total = sum(distribution.values())
    parts = [f"{pod}={count}" for pod, count in sorted(distribution.items())]
    return f"total={total} " + " ".join(parts)


__all__ = [
    "StreamEvent",
    "StreamSummary",
    "MongodCommand",
    "MongodSession",
    "MongosCommand",
    "MongosRemoteRequest",
    "EnvoyStream",
    "EnvoyAccessLogEntry",
    "ClientWireOp",
    "CommandEventListener",
    "TimelineEvent",
    "CursorTree",
    "CursorTreeWireOp",
    "ShardedCursorBranch",
    "ShardedCursorTree",
    "iter_log_lines",
    "parse_mongot_log_line",
    "parse_mongod_log_line",
    "parse_mongod_network_log_line",
    "parse_mongos_log_line",
    "parse_mongos_remote_request_log_line",
    "parse_envoy_debug_log",
    "parse_envoy_access_log_line",
    "read_envoy_access_log",
    "merge_envoy_access_log_into_streams",
    "parse_client_wire_ops",
    "build_stream_summaries",
    "read_mongod_commands",
    "read_mongod_sessions",
    "read_mongos_commands",
    "read_mongos_remote_requests",
    "read_mongot_interceptor_events",
    "correlate_sessions_with_cursors",
    "set_mongod_debug_logs",
    "set_mongos_debug_logs",
    "summarize_cursor_pod_distribution",
    "format_cursor_pod_distribution",
    "print_stream_report",
    "print_mongod_command_report",
    "print_unified_timeline",
    "print_cursor_trees",
    "print_sharded_cursor_trees",
    "render_sharded_cursor_trees",
    "print_lsid_timeline",
    "print_lsids_summary",
    "summarize_lsids_in_window",
    "collect_lsids_in_window",
]
