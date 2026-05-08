"""SQLite-backed log store for the cross-layer analyzer.

Holds one analyzer run's parsed records in an in-memory (or on-disk) SQLite
database. The schema lives in ``store.sql`` next to this module so the
DDL is reviewable as a file. The cursor-tree and unified-timeline builders
(``build_cursor_trees_sql`` / ``build_sharded_cursor_trees_sql`` /
``build_unified_timeline_sql``) read this store; ``test_golden.py`` checks
row-count fidelity against the fixture corpus' ``row_counts.json`` manifest.
"""

from __future__ import annotations

import json
import pathlib
import sqlite3
from dataclasses import asdict, is_dataclass
from datetime import datetime, timedelta
from typing import Any, Iterable, Mapping, Optional

_SCHEMA_FILE = pathlib.Path(__file__).resolve().parent / "store.sql"


def _iso(ts: Any) -> Optional[str]:
    """Coerce datetime / str / None to ISO-8601 TEXT."""
    if isinstance(ts, datetime):
        return ts.isoformat()
    if isinstance(ts, str):
        return ts
    return None


def _as_dict(obj: Any) -> dict:
    """Normalise either a dataclass or a plain mapping to a dict."""
    if is_dataclass(obj):
        return asdict(obj)
    if isinstance(obj, Mapping):
        return dict(obj)
    return obj.__dict__


class LogStore:
    """In-memory SQLite database holding one analyzer run's parsed records.

    Construct, then call ``load_from_parsed_records(...)`` once. Use
    ``query(sql, params) -> list[dict]`` for ad-hoc reads or call the
    purpose-built helpers (``cursor_tree_rows``, ``unified_timeline_rows``).

    ``LogStore(path='/tmp/x.db')`` opens an on-disk DB instead — useful
    when attaching the ``sqlite3`` shell later.
    """

    def __init__(self, path: str = ":memory:") -> None:
        self.conn = sqlite3.connect(path)
        self.conn.row_factory = sqlite3.Row
        self.conn.executescript(_SCHEMA_FILE.read_text())

    # ------------------------------------------------------------------
    # Bulk load
    # ------------------------------------------------------------------

    def load_from_parsed_records(
        self,
        *,
        client_ops: Iterable[Any] = (),
        mongod_commands: Iterable[Any] = (),
        mongod_sessions: Iterable[Any] = (),
        mongos_commands: Iterable[Any] = (),
        mongos_remote_requests: Iterable[Any] = (),
        envoy_streams: Iterable[Any] = (),
        envoy_access: Iterable[Any] = (),
        mongot_streams: Optional[Mapping] = None,
        mongot_batches: Iterable[dict] = (),
        mongot_stream_opens: Iterable[dict] = (),
        mongot_cmds: Iterable[dict] = (),
    ) -> None:
        """Load every layer's parsed records into the SQLite tables.

        Inputs are the same Python objects the parsers in
        ``log_analyzer.analyzer`` already return — dataclasses or plain
        dicts. ``mongot_streams`` is the ``{(pod, stream_id): StreamSummary}``
        shape produced by ``build_stream_summaries``.
        """
        with self.conn:
            self._insert_client_ops(client_ops)
            self._insert_mongod_commands(mongod_commands)
            self._insert_mongod_sessions(mongod_sessions)
            self._insert_mongos_commands(mongos_commands)
            self._insert_mongos_remote_requests(mongos_remote_requests)
            self._insert_envoy_streams(envoy_streams)
            self._insert_envoy_access(envoy_access)
            self._insert_mongot_streams(mongot_streams or {})
            self._insert_mongot_batches(mongot_batches)
            self._insert_mongot_stream_opens(mongot_stream_opens)
            self._insert_mongot_cmds(mongot_cmds)

    # ------------------------------------------------------------------
    # Read helpers
    # ------------------------------------------------------------------

    def query(self, sql: str, params: Optional[tuple] = None) -> list[dict]:
        """Ad-hoc read. Returns list of dicts (column name -> value)."""
        cur = self.conn.execute(sql, params or ())
        return [dict(row) for row in cur.fetchall()]

    def row_counts(self) -> dict[str, int]:
        """One scalar per parser-output table — matches ``row_counts.json``."""
        tables = (
            "client_wire_ops",
            "mongod_commands",
            "mongod_sessions",
            "mongos_commands",
            "mongos_remote_requests",
            "envoy_streams",
            "envoy_access_log",
            "mongot_streams",
            "mongot_batches",
            "mongot_stream_opens",
            "mongot_cmds",
        )
        out: dict[str, int] = {}
        for t in tables:
            out[t] = self.conn.execute(f"SELECT COUNT(*) FROM {t}").fetchone()[0]
        return out

    # ------------------------------------------------------------------
    # Inserts — one per table
    # ------------------------------------------------------------------

    def _insert_client_ops(self, ops):
        rows = []
        for o in ops:
            d = _as_dict(o)
            rows.append(
                (
                    d["request_id"],
                    d["phase"],
                    d["command_name"],
                    _iso(d.get("timestamp")),
                    d.get("server_connection_id"),
                    d.get("lsid"),
                    d.get("cursor_id"),
                    d.get("duration_micros"),
                    d.get("n_returned"),
                    d.get("database_name"),
                    d.get("operation_id"),
                    d.get("failure"),
                )
            )
        self.conn.executemany(
            """INSERT OR REPLACE INTO client_wire_ops
                 (request_id, phase, command_name, timestamp,
                  server_connection_id, lsid, cursor_id, duration_micros,
                  n_returned, database_name, operation_id, failure)
               VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)""",
            rows,
        )

    def _insert_mongod_commands(self, cmds):
        rows = []
        for c in cmds:
            d = _as_dict(c)
            mongot_req = d.get("mongot_request")
            rows.append(
                (
                    d["pod"],
                    _iso(d.get("timestamp")),
                    d["command"],
                    d.get("namespace"),
                    d.get("cursor_id"),
                    d.get("duration_ms"),
                    int(bool(d.get("has_search_stage"))),
                    d.get("lsid"),
                    d.get("server_connection_id"),
                    d.get("ok"),
                    d.get("err_msg"),
                    d.get("err_name"),
                    d.get("err_code"),
                    json.dumps(mongot_req, default=str) if mongot_req else None,
                    json.dumps(d.get("raw") or {}, default=str),
                )
            )
        self.conn.executemany(
            """INSERT INTO mongod_commands
                 (pod, timestamp, command, namespace, cursor_id, duration_ms,
                  has_search_stage, lsid, server_connection_id,
                  ok, err_msg, err_name, err_code, mongot_request, raw)
               VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)""",
            rows,
        )

    def _insert_mongod_sessions(self, sessions):
        rows = []
        for s in sessions:
            d = _as_dict(s)
            rows.append(
                (
                    d["pod"],
                    d["session_id"],
                    d.get("client_id") or "",
                    d.get("remote"),
                    _iso(d.get("opened_at")),
                    _iso(d.get("closed_at")),
                    d.get("status"),
                    d.get("cursor_id"),
                )
            )
        self.conn.executemany(
            """INSERT OR REPLACE INTO mongod_sessions
                 (pod, session_id, client_id, remote, opened_at, closed_at,
                  status, cursor_id)
               VALUES (?, ?, ?, ?, ?, ?, ?, ?)""",
            rows,
        )

    def _insert_mongos_commands(self, cmds):
        rows = []
        for c in cmds:
            d = _as_dict(c)
            rows.append(
                (
                    d["pod"],
                    _iso(d.get("timestamp")),
                    d["command"],
                    d.get("namespace"),
                    d.get("cursor_id"),
                    d.get("duration_ms"),
                    int(bool(d.get("has_search_stage"))),
                    d.get("num_shards"),
                    json.dumps(d.get("shards_targeted") or [], default=str),
                    d.get("lsid"),
                    d.get("server_connection_id"),
                    json.dumps(d.get("raw") or {}, default=str),
                )
            )
        self.conn.executemany(
            """INSERT INTO mongos_commands
                 (pod, timestamp, command, namespace, cursor_id, duration_ms,
                  has_search_stage, num_shards, shards_targeted, lsid,
                  server_connection_id, raw)
               VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)""",
            rows,
        )

    def _insert_mongos_remote_requests(self, reqs):
        rows = []
        for r in reqs:
            d = _as_dict(r)
            rows.append(
                (
                    d["pod"],
                    _iso(d.get("timestamp")),
                    d["request_id"],
                    d["target"],
                    d.get("server_connection_id"),
                )
            )
        self.conn.executemany(
            """INSERT OR REPLACE INTO mongos_remote_requests
                 (pod, timestamp, request_id, target, server_connection_id)
               VALUES (?, ?, ?, ?, ?)""",
            rows,
        )

    def _insert_envoy_streams(self, streams):
        rows = []
        for s in streams:
            d = _as_dict(s)
            rows.append(
                (
                    d["pod"],
                    d["connection_id"],
                    d["stream_id"],
                    d.get("hcm_stream_id"),
                    d.get("path"),
                    d.get("client_id"),
                    _iso(d.get("opened_at")),
                    _iso(d.get("closed_at")),
                    d.get("grpc_status"),
                    int(bool(d.get("rst_stream"))),
                    d.get("inbound_data_frames") or 0,
                    d.get("outbound_data_frames") or 0,
                    d.get("inbound_bytes") or 0,
                    d.get("outbound_bytes") or 0,
                    d.get("upstream_host"),
                    d.get("response_code"),
                    d.get("response_flags"),
                    d.get("access_log_duration_ms"),
                    d.get("access_log_bytes_in"),
                    d.get("access_log_bytes_out"),
                    int(bool(d.get("from_access_log_only"))),
                )
            )
        self.conn.executemany(
            """INSERT INTO envoy_streams
                 (pod, connection_id, stream_id, hcm_stream_id, path,
                  client_id, opened_at, closed_at, grpc_status, rst_stream,
                  inbound_data_frames, outbound_data_frames, inbound_bytes,
                  outbound_bytes, upstream_host, response_code, response_flags,
                  access_log_duration_ms, access_log_bytes_in,
                  access_log_bytes_out, from_access_log_only)
               VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)""",
            rows,
        )

    def _insert_envoy_access(self, entries):
        rows = []
        for e in entries:
            d = _as_dict(e)
            rows.append(
                (
                    d["pod"],
                    _iso(d.get("timestamp")),
                    d.get("client_id"),
                    d.get("upstream_host"),
                    d.get("request_path"),
                    d.get("response_code"),
                    d.get("grpc_status"),
                    d.get("response_flags"),
                    d.get("bytes_received"),
                    d.get("bytes_sent"),
                    d.get("duration_ms"),
                    json.dumps(d.get("raw") or {}, default=str),
                )
            )
        self.conn.executemany(
            """INSERT INTO envoy_access_log
                 (pod, timestamp, client_id, upstream_host, request_path,
                  response_code, grpc_status, response_flags, bytes_received,
                  bytes_sent, duration_ms, raw)
               VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)""",
            rows,
        )

    def _insert_mongot_streams(self, streams):
        rows = []
        # streams is a dict {(pod, stream_id): StreamSummary} per build_stream_summaries.
        items = streams.items() if isinstance(streams, Mapping) else streams
        for key, value in items:
            if isinstance(key, tuple) and len(key) == 2:
                pod, stream_id = key
                d = _as_dict(value)
            else:
                d = _as_dict(value if value is not None else key)
                pod = d["pod"]
                stream_id = d["stream_id"]
            rows.append(
                (
                    pod,
                    stream_id,
                    _iso(d.get("opened_at")),
                    _iso(d.get("closed_at")),
                    d.get("peer"),
                    d.get("grpc_path"),
                    d.get("grpc_status"),
                    d.get("inbound_data_frames") or 0,
                    d.get("outbound_data_frames") or 0,
                    d.get("inbound_bytes") or 0,
                    d.get("outbound_bytes") or 0,
                    int(bool(d.get("rst_stream"))),
                )
            )
        self.conn.executemany(
            """INSERT OR REPLACE INTO mongot_streams
                 (pod, stream_id, opened_at, closed_at, peer, grpc_path,
                  grpc_status, inbound_data_frames, outbound_data_frames,
                  inbound_bytes, outbound_bytes, rst_stream)
               VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)""",
            rows,
        )

    def _insert_mongot_batches(self, batches):
        rows = []
        for b in batches:
            d = dict(b)
            rows.append(
                (
                    d.get("pod"),
                    _iso(d.get("timestamp")),
                    d.get("size"),
                    d.get("cursor_id"),
                    d.get("client_id"),
                )
            )
        self.conn.executemany(
            """INSERT INTO mongot_batches (pod, timestamp, size, cursor_id, client_id)
               VALUES (?, ?, ?, ?, ?)""",
            rows,
        )

    def _insert_mongot_stream_opens(self, opens):
        rows = []
        for o in opens:
            d = dict(o)
            rows.append(
                (
                    d.get("pod"),
                    _iso(d.get("timestamp")),
                    d.get("client_id"),
                    d.get("path"),
                    d.get("cursor_id"),
                    d.get("server_connection_id"),
                )
            )
        self.conn.executemany(
            """INSERT INTO mongot_stream_opens
                 (pod, timestamp, client_id, path, cursor_id, server_connection_id)
               VALUES (?, ?, ?, ?, ?, ?)""",
            rows,
        )

    def _insert_mongot_cmds(self, cmds):
        rows = []
        for c in cmds:
            d = dict(c)
            rows.append(
                (
                    d.get("pod"),
                    _iso(d.get("timestamp")),
                    d.get("command"),
                    d.get("client_id"),
                    d.get("cursor_id"),
                )
            )
        self.conn.executemany(
            """INSERT INTO mongot_cmds (pod, timestamp, command, client_id, cursor_id)
               VALUES (?, ?, ?, ?, ?)""",
            rows,
        )


# ----------------------------------------------------------------------
# SQL-backed cursor-tree builders
# ----------------------------------------------------------------------

_MONGOT_BATCH_MATCH_SLACK_SECONDS = 0.050


def _parse_ts(value: Optional[str]) -> Optional[datetime]:
    if not value:
        return None
    return datetime.fromisoformat(value)


def _demote_intish_float(value):
    """Round-trip int-valued floats back to int.

    Mongod's slow-query record uses int for whole-millisecond durations
    and float for fractional ones. SQLite's REAL column promotes every
    int to float on read, breaking the renderer's str() output. Preserve
    the original Python type by demoting integral floats back to int.
    """
    if isinstance(value, float) and value.is_integer():
        return int(value)
    return value


def build_cursor_trees_sql(store: "LogStore"):
    """SQL-backed equivalent of ``build_cursor_trees``.

    Returns the same ``list[CursorTree]`` shape so the existing
    ``print_cursor_trees`` renderer is unchanged. The Python objects
    referenced inside the trees (``MongodCommand``, ``MongodSession``,
    ``EnvoyStream``, ``StreamSummary`` / mongot dicts) are rehydrated
    from SQL rows — the renderer only reads simple fields off them.

    Mirrors the join semantics in log_analyzer.build_cursor_trees
    so the regression tests' byte-identical golden diff holds.
    """
    # Imported lazily so this module stays importable without the rest
    # of the analyzer package on the path.
    from tests.common.search.log_analyzer.analyzer import (
        CursorTree,
        CursorTreeWireOp,
        EnvoyStream,
        MongodCommand,
        MongodSession,
        StreamSummary,
    )

    conn = store.conn

    # --- 1) Pair client wire ops by request_id (started + succeeded/failed) ---
    paired_rows = conn.execute(
        """
        SELECT
            s.request_id                                              AS request_id,
            s.command_name                                            AS command_name,
            s.timestamp                                               AS started_ts,
            COALESCE(succ.timestamp, fail.timestamp)                  AS final_ts,
            COALESCE(s.cursor_id, succ.cursor_id, fail.cursor_id)     AS cursor_id,
            COALESCE(s.lsid, succ.lsid, fail.lsid)                    AS lsid,
            COALESCE(s.server_connection_id, succ.server_connection_id, fail.server_connection_id)
                                                                      AS server_connection_id,
            COALESCE(succ.duration_micros, fail.duration_micros)      AS duration_micros,
            COALESCE(succ.n_returned, fail.n_returned)                AS n_returned,
            CASE WHEN fail.request_id IS NOT NULL THEN fail.failure END AS failure
        FROM client_wire_ops s
        LEFT JOIN client_wire_ops succ
               ON succ.request_id = s.request_id AND succ.phase = 'succeeded'
        LEFT JOIN client_wire_ops fail
               ON fail.request_id = s.request_id AND fail.phase = 'failed'
        WHERE s.phase = 'started'
          AND s.command_name IN ('aggregate', 'getMore', 'killCursors')
        """
    ).fetchall()
    # Also include cases where there is no started but there is succeeded/failed.
    paired_rows = list(paired_rows)
    seen_requests = {r["request_id"] for r in paired_rows}
    orphan_rows = conn.execute(
        """
        SELECT
            r.request_id                                              AS request_id,
            r.command_name                                            AS command_name,
            NULL                                                      AS started_ts,
            r.timestamp                                               AS final_ts,
            r.cursor_id                                               AS cursor_id,
            r.lsid                                                    AS lsid,
            r.server_connection_id                                    AS server_connection_id,
            r.duration_micros                                         AS duration_micros,
            r.n_returned                                              AS n_returned,
            CASE WHEN r.phase = 'failed' THEN r.failure END           AS failure
        FROM client_wire_ops r
        WHERE r.phase IN ('succeeded', 'failed')
          AND r.command_name IN ('aggregate', 'getMore', 'killCursors')
          AND NOT EXISTS (
              SELECT 1 FROM client_wire_ops s
              WHERE s.request_id = r.request_id AND s.phase = 'started'
          )
        """
    ).fetchall()
    for r in orphan_rows:
        if r["request_id"] not in seen_requests:
            paired_rows.append(r)
            seen_requests.add(r["request_id"])

    collapsed: list[CursorTreeWireOp] = []
    for r in paired_rows:
        collapsed.append(
            CursorTreeWireOp(
                command_name=r["command_name"],
                request_id=r["request_id"],
                client_started=_parse_ts(r["started_ts"]),
                client_succeeded=_parse_ts(r["final_ts"]),
                duration_micros=r["duration_micros"],
                n_returned=r["n_returned"],
                server_connection_id=r["server_connection_id"],
                lsid=r["lsid"],
                cursor_id=r["cursor_id"],
                failure=r["failure"],
            )
        )

    # --- 2) Group by cursor_id ---
    trees_by_cursor: dict[int, CursorTree] = {}
    for op in collapsed:
        if op.cursor_id is None:
            continue
        tree = trees_by_cursor.setdefault(
            op.cursor_id,
            CursorTree(cursor_id=op.cursor_id, client_lsid=op.lsid),
        )
        if tree.client_lsid is None and op.lsid is not None:
            tree.client_lsid = op.lsid
        tree.wire_ops.append(op)

    # --- 3) Lookups: mongod commands, sessions, envoy streams, mongot streams/opens/cmds ---
    def _mc_from_row(row) -> MongodCommand:
        return MongodCommand(
            timestamp=_parse_ts(row["timestamp"]),
            pod=row["pod"],
            command=row["command"],
            namespace=row["namespace"],
            cursor_id=row["cursor_id"],
            duration_ms=_demote_intish_float(row["duration_ms"]),
            has_search_stage=bool(row["has_search_stage"]),
            lsid=row["lsid"],
            server_connection_id=row["server_connection_id"],
            ok=row["ok"],
            err_msg=row["err_msg"],
            err_name=row["err_name"],
            err_code=row["err_code"],
            mongot_request=json.loads(row["mongot_request"]) if row["mongot_request"] else None,
            raw=json.loads(row["raw"]) if row["raw"] else {},
        )

    def _ms_from_row(row) -> MongodSession:
        return MongodSession(
            pod=row["pod"],
            session_id=row["session_id"],
            client_id=row["client_id"] or "",
            remote=row["remote"],
            opened_at=_parse_ts(row["opened_at"]),
            closed_at=_parse_ts(row["closed_at"]),
            status=row["status"],
            cursor_id=row["cursor_id"],
        )

    def _es_from_row(row) -> EnvoyStream:
        return EnvoyStream(
            pod=row["pod"],
            connection_id=row["connection_id"],
            stream_id=row["stream_id"],
            hcm_stream_id=row["hcm_stream_id"],
            path=row["path"],
            client_id=row["client_id"],
            opened_at=_parse_ts(row["opened_at"]),
            closed_at=_parse_ts(row["closed_at"]),
            grpc_status=row["grpc_status"],
            rst_stream=bool(row["rst_stream"]),
            inbound_data_frames=row["inbound_data_frames"] or 0,
            outbound_data_frames=row["outbound_data_frames"] or 0,
            inbound_bytes=row["inbound_bytes"] or 0,
            outbound_bytes=row["outbound_bytes"] or 0,
            upstream_host=row["upstream_host"],
            response_code=row["response_code"],
            response_flags=row["response_flags"],
            access_log_duration_ms=row["access_log_duration_ms"],
            access_log_bytes_in=row["access_log_bytes_in"],
            access_log_bytes_out=row["access_log_bytes_out"],
            from_access_log_only=bool(row["from_access_log_only"]),
        )

    def _mts_from_row(row) -> StreamSummary:
        return StreamSummary(
            pod=row["pod"],
            stream_id=row["stream_id"],
            opened_at=_parse_ts(row["opened_at"]),
            closed_at=_parse_ts(row["closed_at"]),
            peer=row["peer"],
            grpc_path=row["grpc_path"],
            grpc_status=row["grpc_status"],
            inbound_data_frames=row["inbound_data_frames"] or 0,
            outbound_data_frames=row["outbound_data_frames"] or 0,
            inbound_bytes=row["inbound_bytes"] or 0,
            outbound_bytes=row["outbound_bytes"] or 0,
            rst_stream=bool(row["rst_stream"]),
        )

    # Pre-fetch + index.
    all_mongod_cmds = [
        _mc_from_row(r) for r in conn.execute("SELECT * FROM mongod_commands ORDER BY timestamp").fetchall()
    ]
    all_mongod_sessions = [_ms_from_row(r) for r in conn.execute("SELECT * FROM mongod_sessions").fetchall()]
    all_envoy = [_es_from_row(r) for r in conn.execute("SELECT * FROM envoy_streams").fetchall()]
    all_mongot_streams = [_mts_from_row(r) for r in conn.execute("SELECT * FROM mongot_streams").fetchall()]
    all_mongot_opens = [dict(r) for r in conn.execute("SELECT * FROM mongot_stream_opens").fetchall()]
    for o in all_mongot_opens:
        o["timestamp"] = _parse_ts(o.get("timestamp"))
    all_mongot_cmds = [dict(r) for r in conn.execute("SELECT * FROM mongot_cmds").fetchall()]
    all_mongot_batches = [dict(r) for r in conn.execute("SELECT * FROM mongot_batches ORDER BY timestamp").fetchall()]
    for b in all_mongot_batches:
        b["timestamp"] = _parse_ts(b.get("timestamp"))

    # Indexes mirroring build_cursor_trees.
    from collections import defaultdict

    cmds_by_lsid_cursor: dict[tuple[Optional[str], int], list[MongodCommand]] = defaultdict(list)
    cmds_by_cursor: dict[int, list[MongodCommand]] = defaultdict(list)
    search_aggs_by_lsid: dict[str, list[MongodCommand]] = defaultdict(list)
    for cmd in all_mongod_cmds:
        if cmd.cursor_id is not None:
            cmds_by_cursor[cmd.cursor_id].append(cmd)
            cmds_by_lsid_cursor[(cmd.lsid, cmd.cursor_id)].append(cmd)
        # ``search_aggs_by_lsid`` is consulted by the aggregate-branch of the
        # join (line ~752) which keys on ``op.lsid`` alone. Every $search
        # aggregate that opens a cursor (the normal case — cursor_id is
        # NOT None) MUST land here too; otherwise the aggregate's
        # ``mongod_cmd`` join silently fails and the tree renders the
        # aggregate as ``mongod.cmd (no match)`` even though the record is
        # right there in ``cmds_by_cursor``. Pre-fix, the ``elif`` here
        # only caught aggregates whose cursor_id was already None — a
        # very narrow slice. Now we add to BOTH buckets when applicable.
        if cmd.command == "aggregate" and cmd.has_search_stage and cmd.lsid:
            search_aggs_by_lsid[cmd.lsid].append(cmd)
    # Pre-sorted by timestamp in the SELECT above; per-bucket order preserved.

    sessions_by_cursor: dict[int, MongodSession] = {}
    for sess in all_mongod_sessions:
        if sess.cursor_id is not None and sess.cursor_id not in sessions_by_cursor:
            sessions_by_cursor[sess.cursor_id] = sess

    envoy_by_client_id: dict[str, EnvoyStream] = {}
    for es in all_envoy:
        if es.client_id and es.client_id not in envoy_by_client_id:
            envoy_by_client_id[es.client_id] = es

    mongot_open_by_client_id: dict[str, dict] = {}
    for ev in all_mongot_opens:
        cid = ev.get("client_id")
        if cid and cid not in mongot_open_by_client_id:
            mongot_open_by_client_id[cid] = ev

    summaries_by_pod_path: dict[tuple[str, str], list[StreamSummary]] = defaultdict(list)
    for summary in all_mongot_streams:
        if summary.grpc_path:
            summaries_by_pod_path[(summary.pod, summary.grpc_path.lstrip("/"))].append(summary)
    for lst in summaries_by_pod_path.values():
        lst.sort(key=lambda s: s.opened_at or datetime.max)

    # --- 4) Fill in lower-layer fields per tree (mirrors build_cursor_trees). ---
    slack = timedelta(seconds=_MONGOT_BATCH_MATCH_SLACK_SECONDS)

    for tree in trees_by_cursor.values():
        tree.wire_ops.sort(key=lambda op: op.client_started or op.client_succeeded or datetime.max)

        session = sessions_by_cursor.get(tree.cursor_id)
        if session is not None:
            tree.mongod_pod = session.pod
            tree.client_id_uuid = session.client_id or None
        if tree.client_id_uuid is None:
            agg = next((o for o in tree.wire_ops if o.command_name == "aggregate"), None)
            if agg is not None and agg.client_started is not None:
                for sess in all_mongod_sessions:
                    if sess.opened_at is not None and abs((sess.opened_at - agg.client_started).total_seconds()) < 5.0:
                        tree.mongod_pod = tree.mongod_pod or sess.pod
                        tree.client_id_uuid = sess.client_id or None
                        session = sess
                        break

        envoy_stream = envoy_by_client_id.get(tree.client_id_uuid) if tree.client_id_uuid else None

        mongot_open = mongot_open_by_client_id.get(tree.client_id_uuid) if tree.client_id_uuid else None
        if mongot_open is None and envoy_stream is not None and envoy_stream.opened_at:
            for ev in all_mongot_opens:
                ts = ev.get("timestamp")
                if isinstance(ts, datetime) and abs((ts - envoy_stream.opened_at).total_seconds()) < 1.0:
                    mongot_open = ev
                    break

        mongot_stream_summary: Optional[StreamSummary] = None
        if mongot_open is not None:
            pod = mongot_open.get("pod")
            path = (mongot_open.get("path") or "").lstrip("/")
            candidates = summaries_by_pod_path.get((pod, path), []) if pod else []
            mopen_ts = mongot_open.get("timestamp")
            for s in candidates:
                if s.opened_at is None or not isinstance(mopen_ts, datetime):
                    mongot_stream_summary = s
                    break
                if abs((s.opened_at - mopen_ts).total_seconds()) < 2.0:
                    mongot_stream_summary = s
                    break
        elif envoy_stream is not None and envoy_stream.opened_at:
            best: Optional[StreamSummary] = None
            best_delta = float("inf")
            for summary in all_mongot_streams:
                if summary.opened_at is None:
                    continue
                delta = abs((summary.opened_at - envoy_stream.opened_at).total_seconds())
                if delta < best_delta and delta < 2.0:
                    best = summary
                    best_delta = delta
            mongot_stream_summary = best

        if mongot_stream_summary is not None:
            tree.mongot_pod = mongot_stream_summary.pod
            tree.mongot_stream_id = mongot_stream_summary.stream_id

        cursor_batches: list[dict] = []
        if tree.mongot_pod is not None:
            cursor_batches = [
                b
                for b in all_mongot_batches
                if b.get("pod") == tree.mongot_pod and isinstance(b.get("timestamp"), datetime)
            ]
        if mongot_stream_summary is not None:
            life_start = mongot_stream_summary.opened_at or datetime.min
            life_end = mongot_stream_summary.closed_at or datetime.max
            cursor_batches = [b for b in cursor_batches if life_start <= b["timestamp"] <= life_end]

        for op in tree.wire_ops:
            if op.command_name == "aggregate" and op.lsid is not None:
                candidates = search_aggs_by_lsid.get(op.lsid, [])
                op.mongod_cmd = _closest_within_dataclass(candidates, op.client_started, op.client_succeeded)
            else:
                if op.lsid is not None and op.cursor_id is not None:
                    candidates = [
                        c for c in cmds_by_lsid_cursor.get((op.lsid, op.cursor_id), []) if c.command == op.command_name
                    ]
                    op.mongod_cmd = _closest_within_dataclass(candidates, op.client_started, op.client_succeeded)
                if op.mongod_cmd is None and op.cursor_id is not None:
                    candidates = [c for c in cmds_by_cursor.get(op.cursor_id, []) if c.command == op.command_name]
                    op.mongod_cmd = _closest_within_dataclass(candidates, op.client_started, op.client_succeeded)

            if op.command_name == "aggregate":
                op.mongod_session_open = session
                op.envoy_stream = envoy_stream
                op.mongot_stream_open = mongot_open
                for ev in all_mongot_cmds:
                    if ev.get("command") == "SearchCommand" and ev.get("cursor_id") == op.cursor_id:
                        op.mongot_cmd = ev
                        break

            if op.command_name == "killCursors":
                if session is not None and session.closed_at is not None:
                    op.mongod_session_close = session
                if mongot_stream_summary is not None and mongot_stream_summary.closed_at:
                    op.mongot_stream_close = mongot_stream_summary

            if op.client_started is not None and op.client_succeeded is not None:
                lo = op.client_started - slack
                hi = op.client_succeeded + slack
                matched = [b for b in cursor_batches if lo <= b["timestamp"] <= hi]
                if matched:
                    op.mongot_batches.extend(matched)
                    op.served_fresh_from_mongot = True

    # --- 5) Filter to $search trees only. ---
    final: list[CursorTree] = []
    for tree in trees_by_cursor.values():
        agg = next((o for o in tree.wire_ops if o.command_name == "aggregate"), None)
        if agg is None:
            continue
        if agg.mongod_cmd is None or not agg.mongod_cmd.has_search_stage:
            if tree.mongot_pod is None:
                continue
        final.append(tree)

    def _first_ts(tree):
        for op in tree.wire_ops:
            if op.client_started is not None:
                return op.client_started
            if op.client_succeeded is not None:
                return op.client_succeeded
        return datetime.max

    final.sort(key=_first_ts)
    return final


def build_sharded_cursor_trees_sql(
    store: "LogStore",
    *,
    shard_pod_prefixes: dict,
    shard_mongot_pod_prefixes: Optional[dict] = None,
):
    """Build per-cursor sharded fanout trees from the log store.

    Returns a ``list[ShardedCursorTree]`` consumed by
    ``print_sharded_cursor_trees``. Joins are: per-shard time-window for
    the mongos fanout, ±5s session match, ±2s mongot fallback.
    """
    from tests.common.search.log_analyzer.analyzer import (
        CursorTreeWireOp,
        EnvoyStream,
        MongodCommand,
        MongodSession,
        MongosCommand,
        MongosRemoteRequest,
        ShardedCursorBranch,
        ShardedCursorTree,
        StreamSummary,
        _shard_from_target,
    )

    conn = store.conn

    # 1) Pair client wire ops (same as RS path).
    paired = conn.execute(
        """
        SELECT
            s.request_id                                              AS request_id,
            s.command_name                                            AS command_name,
            s.timestamp                                               AS started_ts,
            COALESCE(succ.timestamp, fail.timestamp)                  AS final_ts,
            COALESCE(s.cursor_id, succ.cursor_id, fail.cursor_id)     AS cursor_id,
            COALESCE(s.lsid, succ.lsid, fail.lsid)                    AS lsid,
            COALESCE(s.server_connection_id, succ.server_connection_id, fail.server_connection_id)
                                                                      AS server_connection_id,
            COALESCE(succ.duration_micros, fail.duration_micros)      AS duration_micros,
            COALESCE(succ.n_returned, fail.n_returned)                AS n_returned,
            CASE WHEN fail.request_id IS NOT NULL THEN fail.failure END AS failure
        FROM client_wire_ops s
        LEFT JOIN client_wire_ops succ
               ON succ.request_id = s.request_id AND succ.phase = 'succeeded'
        LEFT JOIN client_wire_ops fail
               ON fail.request_id = s.request_id AND fail.phase = 'failed'
        WHERE s.phase = 'started'
          AND s.command_name IN ('aggregate', 'getMore', 'killCursors')
        """
    ).fetchall()
    paired = list(paired)
    seen = {r["request_id"] for r in paired}
    for r in conn.execute(
        """
        SELECT
            r.request_id                                              AS request_id,
            r.command_name                                            AS command_name,
            NULL                                                      AS started_ts,
            r.timestamp                                               AS final_ts,
            r.cursor_id                                               AS cursor_id,
            r.lsid                                                    AS lsid,
            r.server_connection_id                                    AS server_connection_id,
            r.duration_micros                                         AS duration_micros,
            r.n_returned                                              AS n_returned,
            CASE WHEN r.phase = 'failed' THEN r.failure END           AS failure
        FROM client_wire_ops r
        WHERE r.phase IN ('succeeded', 'failed')
          AND r.command_name IN ('aggregate', 'getMore', 'killCursors')
          AND NOT EXISTS (
              SELECT 1 FROM client_wire_ops s
              WHERE s.request_id = r.request_id AND s.phase = 'started'
          )
        """
    ).fetchall():
        if r["request_id"] not in seen:
            paired.append(r)
            seen.add(r["request_id"])

    collapsed: list[CursorTreeWireOp] = []
    for r in paired:
        collapsed.append(
            CursorTreeWireOp(
                command_name=r["command_name"],
                request_id=r["request_id"],
                client_started=_parse_ts(r["started_ts"]),
                client_succeeded=_parse_ts(r["final_ts"]),
                duration_micros=r["duration_micros"],
                n_returned=r["n_returned"],
                server_connection_id=r["server_connection_id"],
                lsid=r["lsid"],
                cursor_id=r["cursor_id"],
                failure=r["failure"],
            )
        )

    # 2) Load mongos + mongod + sessions + envoy + mongot tables.
    def _mongos_cmd_from_row(row) -> MongosCommand:
        return MongosCommand(
            timestamp=_parse_ts(row["timestamp"]),
            pod=row["pod"],
            command=row["command"],
            namespace=row["namespace"],
            cursor_id=row["cursor_id"],
            duration_ms=_demote_intish_float(row["duration_ms"]),
            has_search_stage=bool(row["has_search_stage"]),
            num_shards=row["num_shards"],
            shards_targeted=json.loads(row["shards_targeted"]) if row["shards_targeted"] else [],
            lsid=row["lsid"],
            server_connection_id=row["server_connection_id"],
            raw=json.loads(row["raw"]) if row["raw"] else {},
        )

    def _mc_from_row(row) -> MongodCommand:
        return MongodCommand(
            timestamp=_parse_ts(row["timestamp"]),
            pod=row["pod"],
            command=row["command"],
            namespace=row["namespace"],
            cursor_id=row["cursor_id"],
            duration_ms=_demote_intish_float(row["duration_ms"]),
            has_search_stage=bool(row["has_search_stage"]),
            lsid=row["lsid"],
            server_connection_id=row["server_connection_id"],
            ok=row["ok"],
            err_msg=row["err_msg"],
            err_name=row["err_name"],
            err_code=row["err_code"],
            mongot_request=json.loads(row["mongot_request"]) if row["mongot_request"] else None,
            raw=json.loads(row["raw"]) if row["raw"] else {},
        )

    def _ms_from_row(row) -> MongodSession:
        return MongodSession(
            pod=row["pod"],
            session_id=row["session_id"],
            client_id=row["client_id"] or "",
            remote=row["remote"],
            opened_at=_parse_ts(row["opened_at"]),
            closed_at=_parse_ts(row["closed_at"]),
            status=row["status"],
            cursor_id=row["cursor_id"],
        )

    def _es_from_row(row) -> EnvoyStream:
        return EnvoyStream(
            pod=row["pod"],
            connection_id=row["connection_id"],
            stream_id=row["stream_id"],
            hcm_stream_id=row["hcm_stream_id"],
            path=row["path"],
            client_id=row["client_id"],
            opened_at=_parse_ts(row["opened_at"]),
            closed_at=_parse_ts(row["closed_at"]),
            grpc_status=row["grpc_status"],
            rst_stream=bool(row["rst_stream"]),
            inbound_data_frames=row["inbound_data_frames"] or 0,
            outbound_data_frames=row["outbound_data_frames"] or 0,
            inbound_bytes=row["inbound_bytes"] or 0,
            outbound_bytes=row["outbound_bytes"] or 0,
            upstream_host=row["upstream_host"],
            response_code=row["response_code"],
            response_flags=row["response_flags"],
            access_log_duration_ms=_demote_intish_float(row["access_log_duration_ms"]),
            access_log_bytes_in=row["access_log_bytes_in"],
            access_log_bytes_out=row["access_log_bytes_out"],
            from_access_log_only=bool(row["from_access_log_only"]),
        )

    def _mts_from_row(row) -> StreamSummary:
        return StreamSummary(
            pod=row["pod"],
            stream_id=row["stream_id"],
            opened_at=_parse_ts(row["opened_at"]),
            closed_at=_parse_ts(row["closed_at"]),
            peer=row["peer"],
            grpc_path=row["grpc_path"],
            grpc_status=row["grpc_status"],
            inbound_data_frames=row["inbound_data_frames"] or 0,
            outbound_data_frames=row["outbound_data_frames"] or 0,
            inbound_bytes=row["inbound_bytes"] or 0,
            outbound_bytes=row["outbound_bytes"] or 0,
            rst_stream=bool(row["rst_stream"]),
        )

    mongos_cmds = [
        _mongos_cmd_from_row(r) for r in conn.execute("SELECT * FROM mongos_commands ORDER BY timestamp").fetchall()
    ]
    mongos_reqs = [
        MongosRemoteRequest(
            timestamp=_parse_ts(r["timestamp"]),
            pod=r["pod"],
            request_id=r["request_id"],
            target=r["target"],
            server_connection_id=r["server_connection_id"],
        )
        for r in conn.execute("SELECT * FROM mongos_remote_requests ORDER BY timestamp").fetchall()
    ]
    mongod_cmds = [_mc_from_row(r) for r in conn.execute("SELECT * FROM mongod_commands ORDER BY timestamp").fetchall()]
    mongod_sessions = [_ms_from_row(r) for r in conn.execute("SELECT * FROM mongod_sessions").fetchall()]
    envoy_streams = [_es_from_row(r) for r in conn.execute("SELECT * FROM envoy_streams").fetchall()]
    mongot_streams_list = [_mts_from_row(r) for r in conn.execute("SELECT * FROM mongot_streams").fetchall()]
    mongot_streams_dict = {(s.pod, s.stream_id): s for s in mongot_streams_list}
    mongot_opens = [dict(r) for r in conn.execute("SELECT * FROM mongot_stream_opens").fetchall()]
    for o in mongot_opens:
        o["timestamp"] = _parse_ts(o.get("timestamp"))
    mongot_batches = [dict(r) for r in conn.execute("SELECT * FROM mongot_batches ORDER BY timestamp").fetchall()]
    for b in mongot_batches:
        b["timestamp"] = _parse_ts(b.get("timestamp"))

    # 3) Indexes mirroring build_sharded_cursor_trees.
    from collections import defaultdict
    from datetime import timedelta

    mongos_by_top_cursor: dict[int, list] = defaultdict(list)
    mongos_by_lsid: dict[str, list] = defaultdict(list)
    for mc in mongos_cmds:
        if not mc.has_search_stage and mc.command == "aggregate":
            continue
        if mc.cursor_id is not None:
            mongos_by_top_cursor[mc.cursor_id].append(mc)
        if mc.lsid is not None:
            mongos_by_lsid[mc.lsid].append(mc)
    for lst in mongos_by_top_cursor.values():
        lst.sort(key=lambda m: m.timestamp or datetime.max)
    for lst in mongos_by_lsid.values():
        lst.sort(key=lambda m: m.timestamp or datetime.max)

    trees: dict[int, ShardedCursorTree] = {}
    for op in collapsed:
        if op.cursor_id is None:
            continue
        tree = trees.setdefault(
            op.cursor_id,
            ShardedCursorTree(top_cursor_id=op.cursor_id, client_lsid=op.lsid),
        )
        if tree.client_lsid is None and op.lsid is not None:
            tree.client_lsid = op.lsid
        tree.wire_ops.append(op)

    shard_mongod_aggs_by_shard: dict[str, list] = defaultdict(list)
    shard_mongod_cmds_by_shard_cursor: dict[tuple, list] = defaultdict(list)
    for cmd in mongod_cmds:
        shard = _shard_from_target(cmd.pod, shard_pod_prefixes)
        if shard is None:
            continue
        if cmd.command == "aggregate" and cmd.has_search_stage:
            shard_mongod_aggs_by_shard[shard].append(cmd)
        if cmd.cursor_id is not None:
            shard_mongod_cmds_by_shard_cursor[(shard, cmd.cursor_id)].append(cmd)
    for lst in shard_mongod_aggs_by_shard.values():
        lst.sort(key=lambda c: c.timestamp or datetime.max)
    for lst in shard_mongod_cmds_by_shard_cursor.values():
        lst.sort(key=lambda c: c.timestamp or datetime.max)

    envoy_by_client_id: dict[str, EnvoyStream] = {}
    for es in envoy_streams:
        if es.client_id and es.client_id not in envoy_by_client_id:
            envoy_by_client_id[es.client_id] = es

    mongot_open_by_client_id: dict[str, dict] = {}
    for ev in mongot_opens:
        cid = ev.get("client_id")
        if cid and cid not in mongot_open_by_client_id:
            mongot_open_by_client_id[cid] = ev

    summaries_by_pod_path: dict[tuple[str, str], list[StreamSummary]] = defaultdict(list)
    for summary in mongot_streams_list:
        if summary.grpc_path:
            summaries_by_pod_path[(summary.pod, summary.grpc_path.lstrip("/"))].append(summary)
    for lst in summaries_by_pod_path.values():
        lst.sort(key=lambda s: s.opened_at or datetime.max)

    # 4) Per-tree fanout (mirrors build_sharded_cursor_trees).
    for tree in trees.values():
        tree.wire_ops.sort(key=lambda op: op.client_started or op.client_succeeded or datetime.max)
        tree.mongos_commands = mongos_by_top_cursor.get(tree.top_cursor_id, [])
        if not tree.mongos_commands and tree.client_lsid:
            tree.mongos_commands = mongos_by_lsid.get(tree.client_lsid, [])
        if tree.mongos_commands:
            tree.mongos_pod = tree.mongos_commands[0].pod
            tree.num_shards = next((mc.num_shards for mc in tree.mongos_commands if mc.num_shards), None)

        tree_start = tree.mongos_commands[0].timestamp if tree.mongos_commands else None
        tree_end = tree.mongos_commands[-1].timestamp if tree.mongos_commands else None
        win_lo = (tree_start - timedelta(seconds=3)) if tree_start else datetime.min
        win_hi = (tree_end + timedelta(seconds=3)) if tree_end else datetime.max

        for shard_name in shard_pod_prefixes:
            shard_aggs = [
                a
                for a in shard_mongod_aggs_by_shard.get(shard_name, [])
                if a.timestamp is not None and win_lo <= a.timestamp <= win_hi
            ]
            if not shard_aggs:
                continue
            agg = shard_aggs[0]
            sub_cursor_id = agg.cursor_id
            if sub_cursor_id is None:
                for (sh, cid), lst in shard_mongod_cmds_by_shard_cursor.items():
                    if sh != shard_name:
                        continue
                    for c in lst:
                        if c.command == "getMore" and c.timestamp is not None and win_lo <= c.timestamp <= win_hi:
                            sub_cursor_id = cid
                            break
                    if sub_cursor_id is not None:
                        break
            branch = ShardedCursorBranch(
                shard_name=shard_name,
                mongod_pod=agg.pod,
                sub_cursor_id=sub_cursor_id,
                mongod_commands=list(shard_aggs),
            )
            if sub_cursor_id is not None:
                more = shard_mongod_cmds_by_shard_cursor.get((shard_name, sub_cursor_id), [])
                seen_ts = {c.timestamp for c in branch.mongod_commands}
                for c in more:
                    if c.timestamp not in seen_ts and c.timestamp is not None and win_lo <= c.timestamp <= win_hi:
                        branch.mongod_commands.append(c)
                branch.mongod_commands.sort(key=lambda c: c.timestamp or datetime.max)

            for req in mongos_reqs:
                shard_for_req = _shard_from_target(req.target, shard_pod_prefixes)
                if shard_for_req == shard_name:
                    branch.target_host = req.target
                    break

            if branch.mongod_pod and agg.timestamp:
                best: Optional[MongodSession] = None
                best_delta = float("inf")
                for sess in mongod_sessions:
                    if sess.pod != branch.mongod_pod or sess.opened_at is None:
                        continue
                    delta = abs((sess.opened_at - agg.timestamp).total_seconds())
                    if delta < best_delta and delta < 5.0:
                        best = sess
                        best_delta = delta
                branch.mongod_session = best
                if best is not None:
                    branch.client_id_uuid = best.client_id or None

            if branch.client_id_uuid:
                branch.envoy_stream = envoy_by_client_id.get(branch.client_id_uuid)
                branch.mongot_stream_open = mongot_open_by_client_id.get(branch.client_id_uuid)
            if branch.mongot_stream_open is not None:
                mso = branch.mongot_stream_open
                pod = mso.get("pod")
                path = (mso.get("path") or "").lstrip("/")
                cands = summaries_by_pod_path.get((pod, path), []) if pod else []
                if cands:
                    branch.mongot_stream_summary = cands[0]
                    branch.mongot_pod = cands[0].pod
                    branch.mongot_stream_id = cands[0].stream_id
                else:
                    branch.mongot_pod = pod
            elif branch.envoy_stream is not None and branch.envoy_stream.opened_at:
                best_s: Optional[StreamSummary] = None
                best_delta_s = float("inf")
                for summary in mongot_streams_list:
                    if summary.opened_at is None:
                        continue
                    d = abs((summary.opened_at - branch.envoy_stream.opened_at).total_seconds())
                    if d < best_delta_s and d < 2.0:
                        best_s = summary
                        best_delta_s = d
                if best_s is not None:
                    branch.mongot_stream_summary = best_s
                    branch.mongot_pod = best_s.pod
                    branch.mongot_stream_id = best_s.stream_id

            if shard_mongot_pod_prefixes and shard_name in shard_mongot_pod_prefixes:
                mongot_prefix = shard_mongot_pod_prefixes[shard_name]
                shard_pod_batches = sorted(
                    [
                        b
                        for b in mongot_batches
                        if isinstance(b.get("pod"), str)
                        and b["pod"].startswith(mongot_prefix)
                        and isinstance(b.get("timestamp"), datetime)
                        and win_lo <= b["timestamp"] <= win_hi
                    ],
                    key=lambda b: b["timestamp"],
                )
                if shard_pod_batches:
                    if not branch.mongot_pod:
                        branch.mongot_pod = shard_pod_batches[0]["pod"]
                    if not branch.mongot_batches:
                        branch.mongot_batches = shard_pod_batches
            if not branch.mongot_batches and branch.mongot_pod:
                start = (
                    branch.mongot_stream_summary.opened_at
                    if branch.mongot_stream_summary
                    else (agg.timestamp or datetime.min)
                ) or datetime.min
                end = (
                    branch.mongot_stream_summary.closed_at if branch.mongot_stream_summary else datetime.max
                ) or datetime.max
                branch.mongot_batches = sorted(
                    [
                        b
                        for b in mongot_batches
                        if b.get("pod") == branch.mongot_pod
                        and isinstance(b.get("timestamp"), datetime)
                        and start <= b["timestamp"] <= end
                    ],
                    key=lambda b: b["timestamp"],
                )

            tree.branches.append(branch)

        tree.branches.sort(key=lambda b: b.shard_name)

    out = sorted(
        trees.values(),
        key=lambda t: (t.mongos_commands[0].timestamp if t.mongos_commands else datetime.max),
    )
    return out


def build_unified_timeline_sql(store: "LogStore", *, include_client_ops: bool = True):
    """SQL-backed equivalent of ``unified_timeline``.

    Returns a ``list[TimelineEvent]`` ordered exactly as the Python
    builder ordered it: ``(timestamp, layer, pod or '')``.

    Mongot interceptor "extras" (MONGOT_STREAM_OPEN / MONGOT_CMD) are
    represented as rows in the ``mongot_stream_opens`` / ``mongot_cmds``
    tables and emitted under the layers the original builder used
    (``mongot.frame`` / ``mongot.batch``) so the renderer's per-layer
    pod-tag rendering is preserved.

    ``include_client_ops`` lets a caller skip client_wire_ops events —
    used by the standalone CLI which synthesises wire-op events from
    server-side COMMAND records under a different kind label
    (``<cmd>.synth``) and adds them itself.
    """
    from tests.common.search.log_analyzer.analyzer import TimelineEvent

    conn = store.conn
    events: list[TimelineEvent] = []

    if include_client_ops:
        for r in conn.execute("SELECT * FROM client_wire_ops").fetchall():
            ts = _parse_ts(r["timestamp"])
            if ts is None:
                continue
            events.append(
                TimelineEvent(
                    timestamp=ts,
                    layer="client",
                    pod=None,
                    kind=f"{r['command_name']}.{r['phase']}",
                    lsid=r["lsid"],
                    cursor_id=r["cursor_id"],
                    server_connection_id=r["server_connection_id"],
                    details={
                        "request_id": r["request_id"],
                        "duration_micros": r["duration_micros"],
                        "n_returned": r["n_returned"],
                        "failure": r["failure"],
                    },
                )
            )

    # mongod sessions: one event per open/close
    for r in conn.execute("SELECT * FROM mongod_sessions").fetchall():
        opened = _parse_ts(r["opened_at"])
        if opened is not None:
            events.append(
                TimelineEvent(
                    timestamp=opened,
                    layer="mongod.net",
                    pod=r["pod"],
                    kind="session_open",
                    client_id=r["client_id"] or None,
                    cursor_id=r["cursor_id"],
                    session_id=r["session_id"],
                    details={"remote": r["remote"]},
                )
            )
        closed = _parse_ts(r["closed_at"])
        if closed is not None:
            events.append(
                TimelineEvent(
                    timestamp=closed,
                    layer="mongod.net",
                    pod=r["pod"],
                    kind="session_close",
                    client_id=r["client_id"] or None,
                    cursor_id=r["cursor_id"],
                    session_id=r["session_id"],
                    details={"status": r["status"], "remote": r["remote"]},
                )
            )

    # mongod commands
    for r in conn.execute("SELECT * FROM mongod_commands").fetchall():
        ts = _parse_ts(r["timestamp"])
        if ts is None:
            continue
        events.append(
            TimelineEvent(
                timestamp=ts,
                layer="mongod.cmd",
                pod=r["pod"],
                kind=r["command"],
                lsid=r["lsid"],
                cursor_id=r["cursor_id"],
                server_connection_id=r["server_connection_id"],
                details={
                    "namespace": r["namespace"],
                    "duration_ms": _demote_intish_float(r["duration_ms"]),
                    "has_search_stage": bool(r["has_search_stage"]),
                },
            )
        )

    # mongos commands
    for r in conn.execute("SELECT * FROM mongos_commands").fetchall():
        ts = _parse_ts(r["timestamp"])
        if ts is None:
            continue
        events.append(
            TimelineEvent(
                timestamp=ts,
                layer="mongos.cmd",
                pod=r["pod"],
                kind=r["command"],
                lsid=r["lsid"],
                cursor_id=r["cursor_id"],
                server_connection_id=r["server_connection_id"],
                details={
                    "namespace": r["namespace"],
                    "duration_ms": _demote_intish_float(r["duration_ms"]),
                    "has_search_stage": bool(r["has_search_stage"]),
                    "num_shards": r["num_shards"],
                },
            )
        )

    # envoy streams: open + close
    for r in conn.execute("SELECT * FROM envoy_streams").fetchall():
        opened = _parse_ts(r["opened_at"])
        if opened is not None:
            events.append(
                TimelineEvent(
                    timestamp=opened,
                    layer="envoy",
                    pod=r["pod"],
                    kind="stream_open",
                    client_id=r["client_id"],
                    stream_id=r["stream_id"],
                    details={
                        "path": r["path"],
                        "connection_id": r["connection_id"],
                        "hcm_stream_id": r["hcm_stream_id"],
                    },
                )
            )
        closed = _parse_ts(r["closed_at"])
        if closed is not None:
            events.append(
                TimelineEvent(
                    timestamp=closed,
                    layer="envoy",
                    pod=r["pod"],
                    kind="stream_close",
                    client_id=r["client_id"],
                    stream_id=r["stream_id"],
                    details={
                        # Surface the HCM logical stream id alongside the
                        # wire stream_id. stream_id=-1 happens when the
                        # parser sees no DATA frames for the stream (drain,
                        # connect-only proxy events, or access-log-only
                        # entries that didn't get joined with an http2:debug
                        # DATA frame); the HCM id is still a stable per-
                        # connection identifier we can correlate by.
                        "hcm_stream_id": r["hcm_stream_id"],
                        "connection_id": r["connection_id"],
                        "grpc_status": r["grpc_status"],
                        "rst_stream": bool(r["rst_stream"]),
                        "outbound_bytes": r["outbound_bytes"] or 0,
                        "outbound_data_frames": r["outbound_data_frames"] or 0,
                        "upstream_host": r["upstream_host"],
                        "response_code": r["response_code"],
                        "response_flags": r["response_flags"],
                    },
                )
            )

    # mongot streams (Netty-side summaries): open + close
    for r in conn.execute("SELECT * FROM mongot_streams").fetchall():
        opened = _parse_ts(r["opened_at"])
        if opened is not None:
            events.append(
                TimelineEvent(
                    timestamp=opened,
                    layer="mongot.frame",
                    pod=r["pod"],
                    kind="stream_open",
                    stream_id=r["stream_id"],
                    details={"grpc_path": r["grpc_path"], "peer": r["peer"]},
                )
            )
        closed = _parse_ts(r["closed_at"])
        if closed is not None:
            events.append(
                TimelineEvent(
                    timestamp=closed,
                    layer="mongot.frame",
                    pod=r["pod"],
                    kind="stream_close",
                    stream_id=r["stream_id"],
                    details={
                        "grpc_status": r["grpc_status"],
                        "rst_stream": bool(r["rst_stream"]),
                        "outbound_bytes": r["outbound_bytes"] or 0,
                        "outbound_data_frames": r["outbound_data_frames"] or 0,
                    },
                )
            )

    # mongot batches (per-frame batch_prepared events)
    for r in conn.execute("SELECT * FROM mongot_batches").fetchall():
        ts = _parse_ts(r["timestamp"])
        if ts is None:
            continue
        events.append(
            TimelineEvent(
                timestamp=ts,
                layer="mongot.batch",
                pod=r["pod"],
                kind="batch_prepared",
                cursor_id=r["cursor_id"],
                client_id=r["client_id"],
                details={"size": r["size"]},
            )
        )

    # mongot interceptor extras: stream_opens emit on the mongot.frame
    # layer; mongot cmds (SearchCommand / GetMore / KillCursors) emit
    # on the mongot.batch layer.
    for r in conn.execute("SELECT * FROM mongot_stream_opens").fetchall():
        ts = _parse_ts(r["timestamp"])
        if ts is None:
            continue
        events.append(
            TimelineEvent(
                timestamp=ts,
                layer="mongot.frame",
                pod=r["pod"],
                kind="interceptor_stream_open",
                client_id=r["client_id"],
                details={"path": r["path"]},
            )
        )
    for r in conn.execute("SELECT * FROM mongot_cmds").fetchall():
        ts = _parse_ts(r["timestamp"])
        if ts is None:
            continue
        events.append(
            TimelineEvent(
                timestamp=ts,
                layer="mongot.batch",
                pod=r["pod"],
                kind=r["command"] or "command",
                cursor_id=r["cursor_id"],
                client_id=r["client_id"],
            )
        )

    events.sort(key=lambda e: (e.timestamp, e.layer, e.pod or ""))
    return events


def _closest_within_dataclass(candidates, lo, hi):
    """Mirror of ``log_analyzer._closest_within``.

    Kept here so this module doesn't import the entire analyzer just
    for one helper.
    """
    if not candidates:
        return None
    if lo is None and hi is None:
        return candidates[0]
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
