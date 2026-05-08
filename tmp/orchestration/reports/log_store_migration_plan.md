# SQLite analyzer migration — implementation plan

Audience: the engineer (possibly future-me) implementing the migration
in a sequence of small PRs. Pairs with `.generated/log-stack-analysis.md`
(why) and the fixtures under
`docker/mongodb-kubernetes-tests/tests/common/search/testdata/fixtures/`
(regression suite). Branch: lives on a stack off
`lsierant/KUBE-17-connectivity-tool`.

The log-stack analysis recommended DuckDB. After re-reading the join
shapes and the parser dataclasses, **the recommendation in this plan
flips to in-memory SQLite (stdlib)**. Justification under "Goals &
non-goals" below; the column model carries over unchanged.

---

## 1. Goals & non-goals

### Goals

- **Replace the in-Python relational join layer** with SQL views. Three
  functions take ~700 LOC of `defaultdict`+`sort`+`index-then-time-window`
  joins today:
  - `build_cursor_trees`           (~320 LOC; RS per-cursor tree)
  - `build_sharded_cursor_trees`   (~330 LOC; sharded per-cursor tree)
  - `unified_timeline`             (~220 LOC; cross-layer interleave)
- **Keep the parsers unchanged.** The eight `parse_*_log_line`
  functions plus `_unwrap_mongod_record` are the right place for the
  envelope quirks (MCK launcher wrapper, lsid `$uuid`-vs-`$binary`,
  envoy debug-log continuation lines). They become "ETL stage 1: read
  text into dataclasses".
- **Keep the renderers** (`print_cursor_trees`,
  `print_sharded_cursor_trees`, `print_unified_timeline`,
  `print_lsid_timeline`, `print_lsids_summary`,
  `render_sharded_cursor_trees`). They receive a row iterator instead of
  the in-memory tree dataclasses. The box-drawing layout, ANSI color
  palette, and "(cached)" detection all stay.
- **Keep the CLI surface.** `log_analyzer_cli` keeps every flag and
  default; the only addition is `--sql <query>` (PR6) and an opt-in
  `--db-path` for persistent inspection.
- **Lock in the regression target** with the fixture corpus at
  `testdata/fixtures/<scenario>/` + golden output under `golden/`. Every
  intermediate PR must reproduce the golden text byte-for-byte under
  `LANG=C` + `--timeline-max=1000` + `color=False`.

### Non-goals

- Changing log sources or parsers. No new log shippers, no Promtail,
  no Loki, no Vector. The analysis already disqualified those.
- Persisting databases between runs. Default mode is `:memory:`. A
  `--db-path` flag lands in PR6 as an opt-in for ad-hoc Compass-style
  inspection; everything else stays ephemeral.
- Migrations / schemas-as-a-contract. In-memory means each run is
  greenfield; if persistence ever becomes the norm (cross-run analysis
  over a long-lived file) we add an explicit migration step at that
  point.
- Threading model changes. The analyzer is single-threaded today;
  SQLite's connection-per-thread rule is fine for that.
- Replacing the pymongo `CommandListener` integration. `ClientWireOp`
  records still arrive as Python objects from the test harness; the
  CLI keeps synthesising them from server-side COMMAND records.

### Why SQLite (and not DuckDB) on a second read

The log-stack analysis recommended DuckDB on the strength of "OLAP +
single-wheel + window function syntax". On closer inspection of the
e2e test container constraints + the join shapes, **SQLite wins**:

1. **Zero dependency vs one.** SQLite is in the Python stdlib. The
   e2e test image already ships it. DuckDB is a 20-30MB extra wheel
   plus a glibc-vs-musl risk on the EVG container builds (the analysis
   itself flagged this as the one real DuckDB hazard).
2. **Working set is tiny.** Across both clean fixtures the largest
   table is `mongot_batches` at 662 rows (sharded). The whole working
   set is 1-2K rows. SQLite is amply fast at that scale; DuckDB's
   vectorised execution buys nothing.
3. **Same SQL surface for our joins.** All the joins are
   `LEFT JOIN ... USING (k)` with optional `ABS(EPOCH(a.ts - b.ts)) <
   N` time-window predicates. SQLite supports both (timestamps stored
   as TEXT ISO-8601 sort correctly; for arithmetic use
   `julianday(ts) * 86400.0`).
4. **`json1` is bundled.** `json_extract(raw, '$.attr.command.lsid.id')`
   is exactly the surface we need for the `raw` columns. No
   `read_json_auto` magic needed; the existing parsers already
   structure-extract before INSERT.
5. **REPL is `sqlite3` shell**, available on every dev machine, no
   extra tooling. Hand the engineer `--db-path /tmp/x.db --keep-db`
   and they can attach `sqlite3 /tmp/x.db` for ad-hoc poking.

The schema is the contract. If a future workload (cross-run analytics
over months of fixtures) outgrows SQLite, swapping to DuckDB later is
the same DDL.

---

## 2. Schema

One table per parser-output dataclass. Time stored as ISO-8601 TEXT
(string-sortable; `julianday()` for delta arithmetic). JSON-shaped raw
fields stay TEXT and are queried with `json_extract()`. Every join
column gets an index.

```sql
-- Captured by the pymongo CommandListener on the test process side.
-- ``timestamp`` is wall-clock derived from time.monotonic() at INSERT.
CREATE TABLE client_wire_ops (
    request_id           INTEGER NOT NULL,
    phase                TEXT    NOT NULL,   -- 'started' | 'succeeded' | 'failed'
    command_name         TEXT    NOT NULL,
    timestamp            TEXT    NOT NULL,
    server_connection_id INTEGER,
    lsid                 TEXT,
    cursor_id            INTEGER,
    duration_micros      INTEGER,
    n_returned           INTEGER,
    database_name        TEXT,
    operation_id         INTEGER,
    failure              TEXT,
    PRIMARY KEY (request_id, phase)
);
CREATE INDEX idx_cwo_lsid       ON client_wire_ops(lsid);
CREATE INDEX idx_cwo_cursor     ON client_wire_ops(cursor_id);
CREATE INDEX idx_cwo_conn       ON client_wire_ops(server_connection_id);
CREATE INDEX idx_cwo_cmd_ts     ON client_wire_ops(command_name, timestamp);

-- One row per mongod COMMAND slow-query record.
CREATE TABLE mongod_commands (
    pod                  TEXT    NOT NULL,
    timestamp            TEXT    NOT NULL,
    command              TEXT    NOT NULL,
    namespace            TEXT,
    cursor_id            INTEGER,
    duration_ms          REAL,
    has_search_stage     INTEGER NOT NULL DEFAULT 0,
    lsid                 TEXT,
    server_connection_id INTEGER,
    raw                  TEXT,                -- json-shaped slow-query record
    PRIMARY KEY (pod, timestamp, command, COALESCE(cursor_id, -1))
);
CREATE INDEX idx_mc_cursor      ON mongod_commands(cursor_id);
CREATE INDEX idx_mc_lsid_cursor ON mongod_commands(lsid, cursor_id);
CREATE INDEX idx_mc_lsid_cmd    ON mongod_commands(lsid, command);
CREATE INDEX idx_mc_ts          ON mongod_commands(timestamp);

-- gRPC egress session pair (NETWORK id=7401401 open / id=7401403 close).
CREATE TABLE mongod_sessions (
    pod                  TEXT    NOT NULL,
    session_id           INTEGER NOT NULL,
    client_id            TEXT    NOT NULL,    -- UUID — join key out to envoy + mongot
    remote               TEXT,
    opened_at            TEXT    NOT NULL,
    closed_at            TEXT,
    status               TEXT,
    cursor_id            INTEGER,             -- filled by correlate_sessions_with_cursors
    PRIMARY KEY (pod, session_id)
);
CREATE INDEX idx_ms_client      ON mongod_sessions(client_id);
CREATE INDEX idx_ms_cursor      ON mongod_sessions(cursor_id);
CREATE INDEX idx_ms_pod_ts      ON mongod_sessions(pod, opened_at);

-- Mongos COMMAND slow-query records.
CREATE TABLE mongos_commands (
    pod                  TEXT    NOT NULL,
    timestamp            TEXT    NOT NULL,
    command              TEXT    NOT NULL,
    namespace            TEXT,
    cursor_id            INTEGER,             -- mongos top cursor id (attr.cursorid)
    duration_ms          REAL,
    has_search_stage     INTEGER NOT NULL DEFAULT 0,
    num_shards           INTEGER,
    shards_targeted      TEXT,                -- JSON array, often empty
    lsid                 TEXT,
    server_connection_id INTEGER,
    raw                  TEXT,
    PRIMARY KEY (pod, timestamp, command)
);
CREATE INDEX idx_mongos_cursor  ON mongos_commands(cursor_id);
CREATE INDEX idx_mongos_lsid    ON mongos_commands(lsid);
CREATE INDEX idx_mongos_conn    ON mongos_commands(server_connection_id);

-- Mongos NETWORK id=4646300 "Sending request" records (per-shard fanout).
CREATE TABLE mongos_remote_requests (
    pod                  TEXT    NOT NULL,
    timestamp            TEXT    NOT NULL,
    request_id           INTEGER NOT NULL,
    target               TEXT    NOT NULL,    -- '<host>:<port>'
    server_connection_id INTEGER,
    PRIMARY KEY (pod, timestamp, request_id)
);
CREATE INDEX idx_mrr_conn       ON mongos_remote_requests(server_connection_id);
CREATE INDEX idx_mrr_target     ON mongos_remote_requests(target);

-- Envoy HTTP/2 streams (debug log).
CREATE TABLE envoy_streams (
    pod                       TEXT    NOT NULL,
    connection_id             INTEGER NOT NULL,
    stream_id                 INTEGER NOT NULL,   -- HTTP/2 wire stream id
    hcm_stream_id             INTEGER,
    path                      TEXT,
    client_id                 TEXT,               -- 'mongodb-clientid' header
    opened_at                 TEXT,
    closed_at                 TEXT,
    grpc_status               TEXT,
    rst_stream                INTEGER NOT NULL DEFAULT 0,
    inbound_data_frames       INTEGER NOT NULL DEFAULT 0,
    outbound_data_frames      INTEGER NOT NULL DEFAULT 0,
    inbound_bytes             INTEGER NOT NULL DEFAULT 0,
    outbound_bytes            INTEGER NOT NULL DEFAULT 0,
    upstream_host             TEXT,
    response_code             INTEGER,
    response_flags            TEXT,
    access_log_duration_ms    REAL,
    access_log_bytes_in       INTEGER,
    access_log_bytes_out      INTEGER,
    from_access_log_only      INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (pod, connection_id, stream_id)
);
CREATE INDEX idx_es_client      ON envoy_streams(client_id);
CREATE INDEX idx_es_open        ON envoy_streams(opened_at);

-- Always-on stdout envoy access log (one row per stream close).
CREATE TABLE envoy_access_log (
    pod                  TEXT    NOT NULL,
    timestamp            TEXT,
    client_id            TEXT,
    upstream_host        TEXT,
    request_path         TEXT,
    response_code        INTEGER,
    grpc_status          TEXT,
    response_flags       TEXT,
    bytes_received       INTEGER,
    bytes_sent           INTEGER,
    duration_ms          REAL,
    raw                  TEXT
);
CREATE INDEX idx_eal_client     ON envoy_access_log(client_id);
CREATE INDEX idx_eal_ts         ON envoy_access_log(timestamp);

-- Aggregate view of one mongot Netty stream — built by build_stream_summaries.
CREATE TABLE mongot_streams (
    pod                       TEXT    NOT NULL,
    stream_id                 INTEGER NOT NULL,
    opened_at                 TEXT,
    closed_at                 TEXT,
    peer                      TEXT,
    grpc_path                 TEXT,
    grpc_status               TEXT,
    inbound_data_frames       INTEGER NOT NULL DEFAULT 0,
    outbound_data_frames      INTEGER NOT NULL DEFAULT 0,
    inbound_bytes             INTEGER NOT NULL DEFAULT 0,
    outbound_bytes            INTEGER NOT NULL DEFAULT 0,
    rst_stream                INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (pod, stream_id)
);
CREATE INDEX idx_mts_pod_path   ON mongot_streams(pod, grpc_path);
CREATE INDEX idx_mts_open       ON mongot_streams(opened_at);

-- One row per individual mongot frame / batch event (kept for the
-- streams-builder path if we later want to skip the in-Python aggregate
-- and roll it up in SQL via window functions).
CREATE TABLE mongot_stream_events (
    pod                  TEXT    NOT NULL,
    stream_id            INTEGER NOT NULL,
    timestamp            TEXT    NOT NULL,
    kind                 TEXT    NOT NULL,
    length               INTEGER,
    extras               TEXT
);
CREATE INDEX idx_mse_pod_stream ON mongot_stream_events(pod, stream_id);

-- mongot LuceneSearchBatchProducer records.
CREATE TABLE mongot_batches (
    pod                  TEXT    NOT NULL,
    timestamp            TEXT    NOT NULL,
    size                 INTEGER,
    cursor_id            INTEGER,            -- present on patched mongot only
    client_id            TEXT                -- present on patched mongot only
);
CREATE INDEX idx_mb_pod_ts      ON mongot_batches(pod, timestamp);
CREATE INDEX idx_mb_cursor      ON mongot_batches(cursor_id);
CREATE INDEX idx_mb_client      ON mongot_batches(client_id);

-- MongoDbGrpcProtocolInterceptor "stream opened" record.
CREATE TABLE mongot_stream_opens (
    pod                  TEXT    NOT NULL,
    timestamp            TEXT    NOT NULL,
    client_id            TEXT,
    path                 TEXT,
    cursor_id            INTEGER,
    server_connection_id INTEGER
);
CREATE INDEX idx_mso_client     ON mongot_stream_opens(client_id);
CREATE INDEX idx_mso_pod_path   ON mongot_stream_opens(pod, path);

-- Mongot SearchCommand / GetMore / KillCursors records (interceptor).
CREATE TABLE mongot_cmds (
    pod                  TEXT    NOT NULL,
    timestamp            TEXT    NOT NULL,
    command              TEXT,               -- 'SearchCommand' | 'GetMore' | 'KillCursors'
    client_id            TEXT,
    cursor_id            INTEGER
);
CREATE INDEX idx_mtc_cursor     ON mongot_cmds(cursor_id);
CREATE INDEX idx_mtc_client     ON mongot_cmds(client_id);
```

Twelve tables, total schema fits on one screen.

---

## 3. SQL view design

Three views replace the three Python join functions. Each is one
`SELECT` against the tables above. The renderers walk these row sets
in the same order as today's tree-traversal — preserving golden output
byte-for-byte.

### 3.1 `cursor_tree_view` (replaces `build_cursor_trees`)

Today's code groups by `cursor_id` from collapsed `(started,
succeeded)` ClientWireOp pairs, then per cursor pulls in mongod
session / envoy / mongot. The SQL is one row per `(cursor_id,
request_id, command)` — the renderer groups in Python.

```sql
CREATE VIEW cursor_tree_view AS
WITH paired_wire AS (
    SELECT
        s.request_id,
        s.command_name                                        AS command,
        s.cursor_id                                           AS started_cursor_id,
        COALESCE(s.cursor_id, succ.cursor_id)                 AS cursor_id,
        s.lsid                                                AS lsid,
        s.server_connection_id                                AS conn_id,
        s.timestamp                                           AS started_ts,
        succ.timestamp                                        AS succeeded_ts,
        succ.duration_micros                                  AS duration_micros,
        succ.n_returned                                       AS n_returned,
        succ.failure                                          AS failure,
        succ.phase                                            AS final_phase
    FROM client_wire_ops s
    LEFT JOIN client_wire_ops succ
      ON succ.request_id = s.request_id
     AND succ.phase IN ('succeeded', 'failed')
    WHERE s.phase = 'started'
      AND s.command_name IN ('aggregate', 'getMore', 'killCursors')
),
-- The cursor's "anchor row" is the cursor's first aggregate. Tree-level
-- join keys (mongod session, envoy stream, mongot stream) hang off it.
cursor_anchor AS (
    SELECT
        cursor_id,
        MIN(started_ts)                                       AS first_started_ts,
        -- Pick *any* lsid for this cursor — they're stable per cursor.
        MAX(lsid)                                             AS lsid
    FROM paired_wire
    WHERE cursor_id IS NOT NULL
    GROUP BY cursor_id
),
-- Per-cursor mongod session (the one with cursor_id stamped by
-- correlate_sessions_with_cursors), or the nearest open within ±5s of
-- the cursor's first aggregate.
cursor_mongod_session AS (
    SELECT
        a.cursor_id,
        ms.pod                                                AS mongod_pod,
        ms.client_id                                          AS client_id_uuid,
        ms.session_id,
        ms.opened_at,
        ms.closed_at
    FROM cursor_anchor a
    LEFT JOIN mongod_sessions ms
      ON ms.cursor_id = a.cursor_id
    -- Time-window fallback: when correlate didn't fire, pick the closest
    -- session on any pod within ±5s.
    UNION ALL
    SELECT
        a.cursor_id,
        ms.pod,
        ms.client_id,
        ms.session_id,
        ms.opened_at,
        ms.closed_at
    FROM cursor_anchor a
    JOIN mongod_sessions ms
      ON ms.cursor_id IS NULL
     AND ABS((julianday(ms.opened_at) - julianday(a.first_started_ts)) * 86400.0) < 5.0
    WHERE NOT EXISTS (
        SELECT 1 FROM mongod_sessions ms2 WHERE ms2.cursor_id = a.cursor_id
    )
)
SELECT
    pw.request_id,
    pw.command,
    pw.cursor_id,
    pw.lsid,
    pw.conn_id,
    pw.started_ts,
    pw.succeeded_ts,
    pw.duration_micros,
    pw.n_returned,
    pw.failure,
    pw.final_phase,

    cms.mongod_pod,
    cms.client_id_uuid,
    cms.session_id                                            AS mongod_session_id,
    cms.opened_at                                             AS session_opened_at,
    cms.closed_at                                             AS session_closed_at,

    -- Per-op mongod COMMAND match: same (lsid, cursor_id, command) for
    -- getMore/killCursors; for aggregate match by (lsid, command,
    -- has_search_stage) since mongod's aggregate has cursor_id=NULL.
    mc.pod                                                    AS mongod_cmd_pod,
    mc.duration_ms                                            AS mongod_cmd_duration_ms,
    mc.namespace                                              AS mongod_cmd_namespace,
    mc.has_search_stage,

    es.pod                                                    AS envoy_pod,
    es.connection_id                                          AS envoy_connection_id,
    es.stream_id                                              AS envoy_stream_id,
    es.path                                                   AS envoy_path,
    es.upstream_host,
    es.response_code,
    es.response_flags,
    es.grpc_status                                            AS envoy_grpc_status,
    es.rst_stream                                             AS envoy_rst_stream,

    mso.pod                                                   AS mongot_pod,
    mso.path                                                  AS mongot_path,
    mts.stream_id                                             AS mongot_stream_id,
    mts.closed_at                                             AS mongot_stream_closed_at,
    mts.grpc_status                                           AS mongot_grpc_status

FROM paired_wire pw
LEFT JOIN cursor_mongod_session cms USING (cursor_id)
LEFT JOIN mongod_commands mc
       ON ((pw.command = 'aggregate'
            AND mc.command = 'aggregate'
            AND mc.has_search_stage = 1
            AND mc.lsid = pw.lsid)
        OR (pw.command IN ('getMore', 'killCursors')
            AND mc.command = pw.command
            AND mc.cursor_id = pw.cursor_id
            AND mc.lsid = pw.lsid))
      AND mc.timestamp BETWEEN pw.started_ts AND pw.succeeded_ts
LEFT JOIN envoy_streams es
       ON es.client_id = cms.client_id_uuid
LEFT JOIN mongot_stream_opens mso
       ON mso.client_id = cms.client_id_uuid
LEFT JOIN mongot_streams mts
       ON mts.pod = mso.pod
      AND replace(mts.grpc_path, '/', '') = replace(mso.path, '/', '')
;
```

A separate query feeds `mongot_batches` per (op, cursor's mongot pod,
op time window) — keeping batch matching out of the main view because
it expands by ~100x:

```sql
-- Per-wire-op batch hits — caller joins this to cursor_tree_view rows
-- when rendering the "(cached)" / "fresh" annotation.
CREATE VIEW cursor_op_batches AS
SELECT
    pw.request_id,
    pw.command,
    pw.cursor_id,
    b.pod                                                     AS mongot_pod,
    b.timestamp                                               AS batch_ts,
    b.size                                                    AS batch_size
FROM (SELECT request_id, command, cursor_id, started_ts, succeeded_ts
        FROM cursor_tree_view) pw
LEFT JOIN mongot_streams mts ON mts.opened_at = (SELECT MIN(opened_at) FROM mongot_streams)  -- caller binds cursor's mongot pod
LEFT JOIN mongot_batches b
       ON b.pod = mts.pod
      AND b.timestamp BETWEEN datetime(pw.started_ts, '-0.05 seconds')
                          AND datetime(pw.succeeded_ts, '+0.05 seconds');
```

(In PR2 the batch view will use a parameterised join — the caller passes
the cursor's mongot pod from `cursor_tree_view` — rather than the
correlated subquery above. The view in the schema file is the simple
shape; the implementation layer extends it.)

### 3.2 `sharded_cursor_tree_view` (replaces `build_sharded_cursor_trees`)

Two queries: one for the top mongos cursor, one for the per-shard
branches. Easier to render than to express as a single rectangular
view.

```sql
-- Top-of-fanout: one row per (top_cursor_id, lsid, mongos_pod).
CREATE VIEW sharded_cursor_top_view AS
WITH paired_wire AS (
    SELECT
        s.request_id,
        s.command_name AS command,
        s.cursor_id,
        s.lsid,
        s.server_connection_id AS conn_id,
        s.timestamp AS started_ts,
        succ.timestamp AS succeeded_ts,
        succ.duration_micros AS duration_micros,
        succ.n_returned AS n_returned
    FROM client_wire_ops s
    LEFT JOIN client_wire_ops succ
      ON succ.request_id = s.request_id
     AND succ.phase IN ('succeeded', 'failed')
    WHERE s.phase = 'started'
      AND s.command_name IN ('aggregate', 'getMore', 'killCursors')
)
SELECT
    pw.cursor_id                                           AS top_cursor_id,
    pw.lsid                                                AS client_lsid,
    pw.request_id,
    pw.command                                             AS client_command,
    pw.started_ts,
    pw.succeeded_ts,
    pw.duration_micros,
    pw.n_returned,
    mc.pod                                                 AS mongos_pod,
    mc.timestamp                                           AS mongos_ts,
    mc.command                                             AS mongos_command,
    mc.duration_ms                                         AS mongos_duration_ms,
    mc.num_shards
FROM paired_wire pw
LEFT JOIN mongos_commands mc
       ON mc.cursor_id = pw.cursor_id
      AND mc.lsid = pw.lsid
      AND mc.timestamp BETWEEN pw.started_ts AND pw.succeeded_ts
WHERE pw.cursor_id IS NOT NULL
;

-- Per-shard branch: one row per (top_cursor_id, shard). Per-shard
-- mongod aggregate match is by time window because mongos rewrites
-- the lsid on the way down.
--
-- ``shards`` is a CTE seeded by the implementation layer (it knows
-- the resource name and shard count); for the view definition we
-- use a SELECT DISTINCT over observed mongod pods.
CREATE VIEW sharded_cursor_branch_view AS
WITH top AS (SELECT * FROM sharded_cursor_top_view WHERE client_command = 'aggregate'),
     shards AS (
         SELECT DISTINCT
             substr(pod, 1, length(pod) - length(replace(pod, '-', '')))  -- placeholder
                                                                  AS shard_name,
             pod
         FROM mongod_commands
     ),
     shard_aggs AS (
         SELECT
             t.top_cursor_id,
             t.client_lsid,
             t.started_ts                                     AS win_lo,
             t.succeeded_ts                                   AS win_hi,
             s.shard_name,
             mc.pod                                           AS mongod_pod,
             mc.timestamp                                     AS agg_ts,
             mc.cursor_id                                     AS sub_cursor_id
         FROM top t
         CROSS JOIN shards s
         JOIN mongod_commands mc
           ON mc.pod = s.pod
          AND mc.command = 'aggregate'
          AND mc.has_search_stage = 1
          AND mc.timestamp BETWEEN datetime(t.started_ts, '-3 seconds')
                              AND datetime(t.succeeded_ts, '+3 seconds')
     )
SELECT
    sa.top_cursor_id,
    sa.client_lsid,
    sa.shard_name,
    sa.mongod_pod,
    sa.sub_cursor_id,
    -- Best-effort session match per-shard within ±5s of the shard agg.
    ms.client_id                                              AS client_id_uuid,
    ms.session_id                                             AS mongod_session_id,
    es.stream_id                                              AS envoy_stream_id,
    es.upstream_host,
    es.response_code,
    mso.pod                                                   AS mongot_pod,
    mts.stream_id                                             AS mongot_stream_id
FROM shard_aggs sa
LEFT JOIN mongod_sessions ms
       ON ms.pod = sa.mongod_pod
      AND ABS((julianday(ms.opened_at) - julianday(sa.agg_ts)) * 86400.0) < 5.0
LEFT JOIN envoy_streams es
       ON es.client_id = ms.client_id
LEFT JOIN mongot_stream_opens mso
       ON mso.client_id = ms.client_id
LEFT JOIN mongot_streams mts
       ON mts.pod = mso.pod
      AND replace(mts.grpc_path, '/', '') = replace(mso.path, '/', '')
;
```

The "shards" CTE is a placeholder. The implementation layer
(`LogStore.sharded_cursor_branches(...)`) takes the test's
`shard_pod_prefixes` dict, materialises it as a temp table at INSERT
time, and joins against that. See section 4 below.

### 3.3 `unified_timeline_view` (replaces `unified_timeline`)

This is a literal `UNION ALL` of every table coerced to the
`TimelineEvent` shape, ordered by `(timestamp, layer, pod)`.

```sql
CREATE VIEW unified_timeline_view AS
SELECT
    timestamp                                       AS ts,
    'client'                                        AS layer,
    NULL                                            AS pod,
    command_name || '.' || phase                    AS kind,
    NULL                                            AS client_id,
    lsid,
    cursor_id,
    server_connection_id                            AS conn_id,
    NULL                                            AS stream_id,
    NULL                                            AS session_id,
    json_object(
        'request_id', request_id,
        'duration_micros', duration_micros,
        'n_returned', n_returned,
        'failure', failure
    )                                               AS details
FROM client_wire_ops

UNION ALL
SELECT
    opened_at, 'mongod.net', pod, 'session_open', client_id, NULL, cursor_id, NULL, NULL,
    session_id, json_object('remote', remote)
FROM mongod_sessions WHERE opened_at IS NOT NULL

UNION ALL
SELECT
    closed_at, 'mongod.net', pod, 'session_close', client_id, NULL, cursor_id, NULL, NULL,
    session_id, json_object('status', status, 'remote', remote)
FROM mongod_sessions WHERE closed_at IS NOT NULL

UNION ALL
SELECT
    timestamp, 'mongod.cmd', pod, command, NULL, lsid, cursor_id, server_connection_id,
    NULL, NULL,
    json_object('namespace', namespace, 'duration_ms', duration_ms, 'has_search_stage', has_search_stage)
FROM mongod_commands

UNION ALL
SELECT
    timestamp, 'mongos.cmd', pod, command, NULL, lsid, cursor_id, server_connection_id,
    NULL, NULL,
    json_object('namespace', namespace, 'duration_ms', duration_ms,
                'has_search_stage', has_search_stage, 'num_shards', num_shards)
FROM mongos_commands

UNION ALL
SELECT
    opened_at, 'envoy', pod, 'stream_open', client_id, NULL, NULL, NULL, stream_id, NULL,
    json_object('path', path, 'connection_id', connection_id, 'hcm_stream_id', hcm_stream_id)
FROM envoy_streams WHERE opened_at IS NOT NULL

UNION ALL
SELECT
    closed_at, 'envoy', pod, 'stream_close', client_id, NULL, NULL, NULL, stream_id, NULL,
    json_object('grpc_status', grpc_status, 'rst_stream', rst_stream,
                'outbound_bytes', outbound_bytes, 'outbound_data_frames', outbound_data_frames,
                'upstream_host', upstream_host, 'response_code', response_code,
                'response_flags', response_flags)
FROM envoy_streams WHERE closed_at IS NOT NULL

UNION ALL
SELECT
    opened_at, 'mongot.frame', pod, 'stream_open', NULL, NULL, NULL, NULL, stream_id, NULL,
    json_object('grpc_path', grpc_path, 'peer', peer)
FROM mongot_streams WHERE opened_at IS NOT NULL

UNION ALL
SELECT
    closed_at, 'mongot.frame', pod, 'stream_close', NULL, NULL, NULL, NULL, stream_id, NULL,
    json_object('grpc_status', grpc_status, 'rst_stream', rst_stream,
                'outbound_bytes', outbound_bytes, 'outbound_data_frames', outbound_data_frames)
FROM mongot_streams WHERE closed_at IS NOT NULL

UNION ALL
SELECT
    timestamp, 'mongot.batch', pod, 'batch_prepared', client_id, NULL, cursor_id, NULL, NULL,
    NULL, json_object('size', size)
FROM mongot_batches

UNION ALL
SELECT
    timestamp, 'mongot.frame', pod, 'interceptor_stream_open', client_id, NULL, NULL, NULL,
    NULL, NULL, json_object('path', path)
FROM mongot_stream_opens

UNION ALL
SELECT
    timestamp, 'mongot.batch', pod, command, client_id, NULL, cursor_id, NULL, NULL, NULL,
    NULL
FROM mongot_cmds

ORDER BY ts, layer, pod;
```

That single view replaces `unified_timeline()` and the
`_client_ops_as_timeline_events()` synthesiser in
`log_analyzer_cli.py`.

### 3.4 lsid filter (replaces `_events_for_lsid`)

`_events_for_lsid` walks events to fixed point pulling in
`(cursor_id, client_id)` closure. The SQL is a recursive CTE:

```sql
-- Given :target_lsid, return the same belongs-to closure
-- `_events_for_lsid` returns today.
WITH RECURSIVE seed_cursor(cursor_id) AS (
    SELECT DISTINCT cursor_id FROM unified_timeline_view
     WHERE lsid = :target_lsid AND cursor_id IS NOT NULL
),
seed_client(client_id) AS (
    SELECT DISTINCT client_id FROM unified_timeline_view
     WHERE lsid = :target_lsid AND client_id IS NOT NULL
),
closure_cursor(cursor_id) AS (
    SELECT cursor_id FROM seed_cursor
    UNION
    SELECT u.cursor_id
      FROM unified_timeline_view u
      JOIN closure_client cc ON cc.client_id = u.client_id
     WHERE u.cursor_id IS NOT NULL
),
closure_client(client_id) AS (
    SELECT client_id FROM seed_client
    UNION
    SELECT u.client_id
      FROM unified_timeline_view u
      JOIN closure_cursor cc ON cc.cursor_id = u.cursor_id
     WHERE u.client_id IS NOT NULL
)
SELECT * FROM unified_timeline_view
WHERE lsid = :target_lsid
   OR cursor_id IN (SELECT cursor_id FROM closure_cursor)
   OR client_id IN (SELECT client_id FROM closure_client)
ORDER BY ts, layer, pod;
```

SQLite's recursive CTE handles the fixed-point loop in one shot.

---

## 4. Renderer wiring

The renderers each take a row iterator now. Today they take typed
dataclasses; the conversion is mechanical.

### `print_cursor_trees(rows) -> None`

Today: takes `list[CursorTree]`; recursively builds `_RenderNode`
boxes from `tree.wire_ops` + per-op nested fields.

After: takes `list[dict]` (rows from `cursor_tree_view`). The renderer
groups by `cursor_id` (the rows arrive already sorted), threads the
tree-level fields (`mongod_pod`, `client_id_uuid`, `mongot_stream_id`)
from the first row in the group, and walks the rest as per-op nodes.
The "(cached)" annotation gates on whether `cursor_op_batches` returned
a row for this `(request_id, mongot_pod)` pair.

```python
def print_cursor_trees(rows: list[dict]) -> None:
    print(f"\n=== per-cursor tree view — {len(set(r['cursor_id'] for r in rows))} cursor(s) ===")
    by_cursor = defaultdict(list)
    for r in rows:
        by_cursor[r["cursor_id"]].append(r)
    for cursor_id, ops in by_cursor.items():
        head = ops[0]
        print(f"  cursor {cursor_id}  lsid={head['lsid']}  "
              f"mongod={_short(head['mongod_pod'])}  "
              f"mongot={_short(head['mongot_pod'])}  "
              f"clientId={head['client_id_uuid']}  "
              f"streamId={head['mongot_stream_id']}")
        for op in ops:
            _render_op_node(op)  # walks the LEFT JOIN'd columns
```

`_render_op_node` walks the same per-op chain
(`client → mongod.cmd → mongod.net.session → envoy.stream → mongot.*`)
the `_wire_op_to_node` builder does today.

### `print_sharded_cursor_trees(top_rows, branch_rows) -> None`

Top rows from `sharded_cursor_top_view`, branch rows from
`sharded_cursor_branch_view`. Renderer groups branch_rows by
`top_cursor_id` and threads them under the matching top row.

### `print_unified_timeline(rows, max_events) -> None`

Already a flat list — just walk the row sequence printing
`ts layer pod kind ...` exactly like today. The `details` column is
a JSON string; pre-parse with `json.loads()` on entry to the renderer.

### `print_lsid_timeline(rows_for_lsid, lsid, color)` / `print_lsids_summary(rows)`

These already take a flat event list; the row shape from
`unified_timeline_view` is the same as today's `TimelineEvent` — only
the input source changes.

`collect_lsids_in_window` and `summarize_lsids_in_window` become two
small queries:

```sql
SELECT lsid,
       COUNT(*)                         AS num_events,
       MIN(ts)                          AS first_ts,
       MAX(ts)                          AS last_ts,
       MIN(cursor_id)                   AS sample_cursor_id,
       MIN(CASE WHEN layer='mongos.cmd' THEN pod END)
                                        AS mongos_pod,
       MAX(CASE WHEN layer='mongos.cmd'
                THEN json_extract(details, '$.num_shards') END)
                                        AS num_shards
  FROM unified_timeline_view
 WHERE lsid IS NOT NULL
 GROUP BY lsid
 ORDER BY first_ts;
```

---

## 5. Migration PR sequence

### PR1 — Add `LogStore` alongside; no behavior change

- New file `tests/common/search/log_store.py` (~150 LOC, see section 9).
- Schema in `tests/common/search/log_store.sql` so the DDL is
  inspectable and reviewable as a file.
- Hook into the analyzer at a single point: when a test or the CLI
  finishes parsing, it can now optionally also build a `LogStore` and
  attach it to the rendered output for inspection.
- `requirements.txt`: no new dependency (sqlite3 stdlib).
- Tests: a new pytest module `tests/common/search/test_log_store.py`
  parametrised over the two existing fixtures, loads parsers ->
  LogStore and asserts:
  - row counts per table match the `row_counts.json` golden manifest;
  - the recursive lsid CTE returns identical row count to
    `_events_for_lsid` for every detected lsid.
- LOC delta: +400 / -0. No deletions yet.
- Risk: low; new code only.

### PR2 — Rewrite `build_cursor_trees` (RS) over SQL

- Add `LogStore.cursor_tree_rows(self) -> list[dict]` materialising
  `cursor_tree_view`.
- New module-level shim
  `build_cursor_trees_sql(store: LogStore) -> list[dict]` returning the
  same shape `print_cursor_trees` needs.
- Both backends co-exist behind the env var
  `MCK_ANALYZER_BACKEND=python|sql` (default `python`).
- In the regression suite (see section 6) every fixture's golden tree
  output is asserted under BOTH backends. CI diff is the canonical
  signal of a parity defect.
- LOC delta: +120 / -0 (the python builder is not deleted yet).
- Tests: golden output diff for RS fixtures (`rs-paging-clean` +
  every fault-injection RS scenario).
- Risk: medium; the time-window fallbacks (the ±5s session match) are
  the highest-risk pieces. Capture deviations in a `--debug-joins`
  flag that prints the per-cursor row before render.

### PR3 — Rewrite `build_sharded_cursor_trees` over SQL

Same shape as PR2 but for the sharded view. Includes the
`shard_pod_prefixes` temp-table dance described in section 3.2 — the
implementation layer wraps the static shards as a CTE before binding
the view.

- LOC delta: +150 / -0.
- Tests: sharded fixtures' golden outputs under both backends.
- Risk: higher than PR2 because of the per-shard time-window join +
  the lsid-rewrite gotcha. The sharded clean fixture's golden file is
  the contract.

### PR4 — Rewrite `unified_timeline` over SQL

Replaces `unified_timeline()` and the
`_client_ops_as_timeline_events` helper in `log_analyzer_cli.py`.
Adds `LogStore.unified_timeline_rows()`.

- LOC delta: +60 / -0.
- Tests: every fixture's golden `unified_timeline.txt` and every
  per-lsid `lsid_<hex>.txt` under both backends.
- Risk: low. The view is mechanical.

### PR5 — Delete the Python join layer

- Removes `build_cursor_trees`, `build_sharded_cursor_trees`,
  `unified_timeline`, `correlate_sessions_with_cursors` (now a SQL
  `UPDATE`), `_closest_within`, `_first_op`, `_first_wire_op_time`,
  the `defaultdict` indexes inside each, and the
  `MCK_ANALYZER_BACKEND` fallback flag.
- LOC delta: roughly -800 / +0.
- Risk: low — the parity tests in PR2-4 have already proven the
  views; this PR is the cleanup.

### PR6 — Ad-hoc SQL surface

- `log_analyzer_cli --sql 'SELECT ... FROM ...'` runs the query
  against the in-memory DB and prints a tabular result.
- `log_analyzer_cli --repl` drops into `sqlite3` shell on a temp
  on-disk DB path (`--db-path` controls location).
- `log_analyzer_cli --keep-db` skips the temp-file cleanup so the
  user can poke it with their own tool of choice.
- LOC delta: +100 / -0.
- Risk: zero — additive.

### Net

Three+ PRs reach feature parity; PR5 deletes ~800 LOC; PR6 is the
take-home reward — every test author gets an ad-hoc SQL surface for
debugging hairy joins.

---

## 6. Regression strategy

The reference fixtures + goldens already in tree are the contract.

### Fixture layout

```
docker/mongodb-kubernetes-tests/tests/common/search/testdata/fixtures/
├── rs-paging-clean/
│   ├── <pod-name>.log              # one per mongod / mongot / envoy
│   ├── client_wire_ops.jsonl       # pymongo CommandListener serialised
│   ├── metadata.json               # namespace, mdb, topology, pages, ...
│   └── golden/
│       ├── cursor_trees.txt
│       ├── unified_timeline.txt
│       ├── detected_lsids.txt
│       ├── lsid_<hex>.txt          # one per detected lsid
│       └── row_counts.json
├── sharded-paging-clean/...        # same shape, sharded-specific
├── rs-paging-mongot-restart/...    # not captured yet — see "Pending"
├── rs-paging-envoy-restart/...
├── sharded-paging-mongot-restart/...
└── sharded-readiness-probe-disabled/...
```

### Pytest regression module

```python
# tests/common/search/test_analyzer_golden.py
import pathlib, pytest, subprocess, os

FIXTURES = pathlib.Path(__file__).resolve().parent / "testdata" / "fixtures"

@pytest.mark.parametrize("scenario", [d.name for d in FIXTURES.iterdir() if d.is_dir()])
@pytest.mark.parametrize("backend", ["python", "sql"])
def test_analyzer_golden(scenario, backend, tmp_path):
    fixture = FIXTURES / scenario
    env = {**os.environ, "MCK_ANALYZER_BACKEND": backend}
    out_dir = tmp_path / "golden"
    subprocess.check_call([
        sys.executable, "-m",
        "tests.common.search.testdata.generate_golden",
        "--scenario", scenario,
        "--out-dir", str(out_dir),
    ], env=env)
    # Diff each generated file against the committed golden.
    for f in (fixture / "golden").iterdir():
        committed = f.read_text()
        produced = (out_dir / f.name).read_text()
        assert committed == produced, f"{scenario}/{f.name} diverged on backend={backend}"
```

PR2-4 keep both `backend=python` and `backend=sql` green; PR5 deletes
the parametrisation and the env var.

### What "byte identical" means

- Locale: `LANG=C` enforced on the test runner.
- Width: `--timeline-max=1000` so no truncation drift.
- Color: `color=False` (no ANSI) on the lsid timeline.
- Cursor IDs and lsids are stable across re-runs of `generate_golden`
  against the same fixture — confirmed empirically in this session
  by diffing two consecutive generations (zero delta).

If the SQLite backend produces a reviewed, documented delta during the
migration (e.g. a different "(cached)" detection due to a tighter time
window), the golden is updated in the same PR that introduces the
delta — the parity test is the gatekeeper, not the freezer.

---

## 7. Future work after the migration

- **Ad-hoc `--sql` REPL** (PR6 above). The first real user benefit.
- **Persisted DB across runs.** `--db-path` already buys this; the
  follow-up is `ATTACH DATABASE` so you can load N runs side by side
  for cross-run analysis.
- **Compass / mongosh exploration.** Optional `--export-json` writes
  every table as JSONL; `mongoimport` lands them into a side mongo,
  Compass attaches. Useful only when the engineer wants a GUI for
  filtering — the SQL `--repl` covers most needs.
- **`json_extract` on the `raw` column.** For one-off investigations
  ("which slow query had `attr.numYields > 10000`?") you can drop into
  the raw record without re-parsing.
- **`UNION ALL` across fixture corpus.** A fixture-aware loader
  ingests every captured run into a single SQLite, so "what changed
  between mongot 9872ab5089 and the previous release" becomes a
  `GROUP BY mongot_build` query.
- **Move `correlate_sessions_with_cursors` into SQL.** Today it does
  `attr.command.cursorId` → `attr.session.id` matching; in SQL it's
  one `UPDATE mongod_sessions SET cursor_id = (SELECT ...)`.

---

## 8. Risks + open questions

### Schema migration when a new layer lands

A new log surface (say, a cache-layer record from mongot) adds a
table + view branch. In the in-memory mode that's a no-cost change —
every run starts fresh, the new table appears. If we ever persist
SQLite long-term we'd need migrations; that decision is deferred.

### Disk-vs-memory

Default `:memory:` for CI runs (footprint is < 5MB), `--db-path
/tmp/x.db` for ad-hoc explorations. The capture fixture's `.log`
files are themselves the persistence layer for "give me this run a
year from now" — `LogStore` is rebuilt on demand.

### `json_extract` perf

Across the two clean fixtures the `raw` column is ~1KB per row;
`json_extract` on a 2K-row table is sub-millisecond on SQLite. If we
ever hit a perf wall (10K+ rows with deep JSON), promote the
extracted field to a real column at INSERT time. None of the current
parsers need this.

### Threading / connections

SQLite `connection.execute()` is single-threaded per connection.
The analyzer / CLI is single-threaded today, so this is fine.
If a future workload wants concurrent reads, we'd open
`isolation_level=None` + `check_same_thread=False` and let pytest's
xdist workers each open their own connection.

### Time-window join correctness

The risky bit. Today's Python code uses `±5.0s` session match, `±2.0s`
mongot match, `±1.0s` envoy match, `±0.05s` (`_MONGOT_BATCH_MATCH_SLACK_SECONDS`)
batch match. These are all preserved as `BETWEEN` predicates with
`julianday()` arithmetic. The regression suite is the canary — if the
golden output diverges, the predicate is wrong.

### lsid recursion fixed-point

The Python `_events_for_lsid` iterates "up to 8 passes" with an
explicit grow detector. The recursive CTE in section 3.4 implements
the same closure — but SQLite caps recursion depth at 1000 by
default (configurable via `PRAGMA max_recursive_select`). At our
working set size this is never reached, but documenting it for the
day someone loads a 100K-row corpus.

### Existing CLI imports

The current `log_analyzer_cli.py` reaches deeply into the analyzer
module's namespace (e.g. `from ...mongot_log_analyzer import
build_cursor_trees, build_sharded_cursor_trees, unified_timeline,
...`). PR5's deletion will break that import surface; PR2-4 each
swap the import to the SQL-shim entry points.

### Tooling order-of-operations

`pytest --collect-only` must keep working at every PR boundary
(it's our smoke test). That implies the new `log_store.py` module
must NOT depend on `sqlite3` at import time only — the runtime cost
is zero, so just `import sqlite3` at module top is fine. The reason
this is worth flagging: if we ever swap to DuckDB, the `import duckdb`
at module top would suddenly add a wheel resolution step to every
`pytest --collect-only`. SQLite sidesteps that entirely.

---

## 9. Concrete starter for PR1

Working code for the engineer to drop in. Standalone — no imports
from the existing analyzer module — so the schema-load layer is
testable in isolation. Roughly 130 LOC.

```python
# tests/common/search/log_store.py
"""SQLite-backed log store for the cross-layer analyzer."""

from __future__ import annotations

import json
import pathlib
import sqlite3
from contextlib import closing
from dataclasses import asdict, is_dataclass
from datetime import datetime
from typing import Any, Iterable, Optional


_SCHEMA_FILE = pathlib.Path(__file__).resolve().parent / "log_store.sql"


class LogStore:
    """In-memory SQLite database holding one analyzer run's parsed records.

    Constructor opens an in-memory DB and applies the schema. Use
    ``load_*`` methods to populate from the existing parser dataclasses,
    then ``query(sql) -> list[dict]`` for ad-hoc reads or call the
    purpose-built ``cursor_tree_rows()`` / ``unified_timeline_rows()``
    helpers.

    ``LogStore(path='/tmp/x.db')`` opens an on-disk DB instead — useful
    when the engineer wants to attach ``sqlite3`` shell later.
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
        mongot_streams: dict = None,
        mongot_batches: Iterable[dict] = (),
        mongot_stream_opens: Iterable[dict] = (),
        mongot_cmds: Iterable[dict] = (),
    ) -> None:
        """Load every layer's parsed records into the SQLite tables.

        Inputs are the same Python objects the existing parsers return —
        either dataclasses (``MongodCommand``, ``EnvoyStream`` ...) or
        plain dicts (mongot interceptor records). ``mongot_streams`` is
        the ``dict[(pod, sid), StreamSummary]`` shape produced by
        ``build_stream_summaries``.
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
    # Helpers
    # ------------------------------------------------------------------

    def query(self, sql: str, params: Optional[tuple] = None) -> list[dict]:
        """Ad-hoc read. Returns list of dicts (column name -> value)."""
        cur = self.conn.execute(sql, params or ())
        return [dict(row) for row in cur.fetchall()]

    def cursor_tree_rows(self) -> list[dict]:
        return self.query("SELECT * FROM cursor_tree_view")

    def unified_timeline_rows(self) -> list[dict]:
        return self.query("SELECT * FROM unified_timeline_view")

    def detected_lsids(self) -> list[dict]:
        return self.query("""
            SELECT lsid,
                   COUNT(*)                                       AS num_events,
                   MIN(ts)                                        AS first_ts,
                   MAX(ts)                                        AS last_ts,
                   MIN(cursor_id)                                 AS sample_cursor_id,
                   MIN(CASE WHEN layer='mongos.cmd' THEN pod END) AS mongos_pod,
                   MAX(CASE WHEN layer='mongos.cmd'
                            THEN json_extract(details, '$.num_shards') END)
                                                                  AS num_shards
              FROM unified_timeline_view
             WHERE lsid IS NOT NULL
             GROUP BY lsid
             ORDER BY first_ts
        """)

    # ------------------------------------------------------------------
    # Inserts (one per table) — schema column lists kept in sync with the
    # DDL by hand. The existing parser dataclasses' field names match the
    # schema columns; ``_as_dict`` handles either path.
    # ------------------------------------------------------------------

    @staticmethod
    def _as_dict(obj: Any) -> dict:
        if is_dataclass(obj):
            return asdict(obj)
        return dict(obj)

    @staticmethod
    def _iso(ts: Any) -> Optional[str]:
        if isinstance(ts, datetime):
            return ts.isoformat()
        if isinstance(ts, str):
            return ts
        return None

    def _insert_client_ops(self, ops):
        self.conn.executemany(
            """INSERT OR REPLACE INTO client_wire_ops
                 (request_id, phase, command_name, timestamp,
                  server_connection_id, lsid, cursor_id, duration_micros,
                  n_returned, database_name, operation_id, failure)
               VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)""",
            (
                (o.request_id, o.phase, o.command_name, self._iso(o.timestamp),
                 o.server_connection_id, o.lsid, o.cursor_id, o.duration_micros,
                 o.n_returned, o.database_name, o.operation_id, o.failure)
                for o in ops
            ),
        )

    def _insert_mongod_commands(self, cmds):
        self.conn.executemany(
            """INSERT INTO mongod_commands
                 (pod, timestamp, command, namespace, cursor_id, duration_ms,
                  has_search_stage, lsid, server_connection_id, raw)
               VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)""",
            (
                (c.pod, self._iso(c.timestamp), c.command, c.namespace,
                 c.cursor_id, c.duration_ms, int(bool(c.has_search_stage)),
                 c.lsid, c.server_connection_id, json.dumps(c.raw))
                for c in cmds
            ),
        )

    # ... one method per table; omitted for brevity in the plan but each
    # is a 6-10 line `executemany` with the same shape.
```

The companion file `log_store.sql` holds the DDL block from section 2
verbatim plus the views from section 3.

### What "starter" leaves to the engineer

- Six more `_insert_*` methods following the same pattern.
- Wiring `cursor_tree_view` into a renderer entry point (the
  `print_cursor_trees(rows)` shape sketched in section 4).
- Hooking PR1's `LogStore` into the CLI behind a `--use-sqlite` debug
  flag so the engineer can compare both backends interactively.
- A `tests/common/search/test_log_store.py` test class parametrised
  over the fixtures from `testdata/fixtures/` that confirms row
  counts.

---

## 10. Effort estimate

| PR | Net LOC | Engineering days | Risk |
|----|---------|------------------|------|
| PR1 LogStore + schema, no behavior change | +400 / -0 | 1 | low |
| PR2 RS cursor tree over SQL + parity test | +120 / -0 | 1 | medium |
| PR3 Sharded cursor tree over SQL + parity test | +150 / -0 | 1.5 | medium-high |
| PR4 Unified timeline over SQL + parity test | +60 / -0 | 0.5 | low |
| PR5 Delete Python builders + fallback flag | +0 / -800 | 0.5 | low |
| PR6 `--sql` + `--repl` + `--db-path` | +100 / -0 | 0.5 | zero |
| **Total** | **+830 / -800** (~30 net) | **5 days** | **medium** |

Skills needed: Python; SQLite basics (`WITH RECURSIVE`, `julianday`,
`json_extract`); the existing analyzer's join semantics (recap below).

Risk class: medium. The high-risk PRs are 2 and 3 — every time-window
join (±5s mongod session, ±2s mongot, ±1s envoy, ±50ms batch) must be
preserved verbatim. The reference-fixture golden output is the gate.

---

## Appendix A — Join semantics recap (for the implementer)

Lifted from the existing code's join section. Future-me will read
this before writing the SQL.

| Layer A | Layer B | Join key | Type |
|---------|---------|----------|------|
| pymongo started+succeeded | (each other) | `request_id` | deterministic |
| pymongo wire op | mongod COMMAND | `(lsid, cursor_id, command)` or `(lsid, command, has_search_stage)` for aggregate | deterministic |
| mongod COMMAND | mongod NETWORK session | `cursor_id` (via `correlate_sessions_with_cursors`) | deterministic |
| mongod NETWORK session | envoy stream | `client_id` UUID | deterministic |
| envoy stream | mongot stream open | `client_id` UUID | deterministic |
| mongot stream open | mongot stream summary | `(pod, grpc_path)` (lstrip `/`) | deterministic |
| (fallback) any of above | (any) | nearest open within ±5s / ±2s / ±1s | time-window |
| client wire op | mongot batch | wire window ± 50ms on cursor's mongot pod | time-window |
| mongos COMMAND | per-shard mongod COMMAND | namespace + timestamp window ± 3s (mongos rewrites lsid) | time-window |

The time-window fallbacks are the ones that surprise newcomers.
**On the patched mongot build the deterministic keys win**; the
time-window fallbacks exist for the older mongot still present in CI.
Both paths must survive the migration.

---

## Appendix B — Fixture corpus status

Captured this session (verbosity-bumped, both topologies live on
`ls-25`, mongot patch in place):

| Scenario | Topology | Pages | Disk size | Trees | Timeline events | lsids |
|---|---|---|---|---|---|---|
| `rs-paging-clean` | RS | 5 | 588 KB | 1 | 32 | 2 |
| `sharded-paging-clean` | Sharded | 5 | 2.1 MB | 1 | 1352 | 1 |

Capture is byte-deterministic: regenerating the goldens twice and
diffing yields zero deltas.

### Pending (capture during the corresponding e2e re-run)

Each of these is one invocation of
`tests/common/search/testdata/capture_client_wire_ops.py` with the
fault flag set. The capture script handles the disruption itself for
`mongot-restart` / `envoy-restart`; `readiness-probe-disabled` is
deferred to the e2e test path because the operator's reconcile loop
fights direct STS patches (see `kube17-expansion-report.md` open
issues).

```
# rs-paging-mongot-restart
python -m tests.common.search.testdata.capture_client_wire_ops \
    --scenario rs-paging-mongot-restart --topology rs \
    --namespace ls-25 --mdb-name mdb-rs-conn-tool \
    --pages 10 --batch-size 10 \
    --fault mongot-restart --fault-after-pages 3

# rs-paging-envoy-restart
python -m tests.common.search.testdata.capture_client_wire_ops \
    --scenario rs-paging-envoy-restart --topology rs \
    --namespace ls-25 --mdb-name mdb-rs-conn-tool \
    --pages 10 --batch-size 10 \
    --fault envoy-restart --fault-after-pages 3

# sharded-paging-mongot-restart
MCK_ADMIN_USER=mdb-sh-admin-user \
python -m tests.common.search.testdata.capture_client_wire_ops \
    --scenario sharded-paging-mongot-restart --topology sharded \
    --namespace ls-25 --mdb-name mdb-sh-conn-tool \
    --pages 10 --batch-size 10 \
    --fault mongot-restart --fault-after-pages 3

# sharded-readiness-probe-disabled (capture-then-test path)
#   1. Drive the disruption via the e2e test:
#        scripts/dev/e2e_run.sh e2e_search_connectivity_tool_sharded \
#            -k test_query_fails_when_envoy_endpoints_removed_for_one_shard
#   2. Immediately after, capture the resulting log window:
#        --scenario sharded-readiness-probe-disabled --topology sharded
#        --namespace ls-25 --mdb-name mdb-sh-conn-tool --pages 0
#        (--pages 0 is a marker: pull logs only, no query).
```

`--pages 0` is the read-only mode for capturing fault scenarios where
the disruption is driven outside this script.

---

## Appendix C — Files in this branch

- `docker/mongodb-kubernetes-tests/tests/common/search/testdata/capture_client_wire_ops.py`
  — fixture capture script.
- `docker/mongodb-kubernetes-tests/tests/common/search/testdata/generate_golden.py`
  — golden output generator (also serves as the regression-test runner
  scaffolding for PR1).
- `docker/mongodb-kubernetes-tests/tests/common/search/testdata/fixtures/rs-paging-clean/`
- `docker/mongodb-kubernetes-tests/tests/common/search/testdata/fixtures/sharded-paging-clean/`
- `.generated/sqlite-analyzer-plan.md` (this file).
- `.generated/log-stack-analysis.md` (the "why SQL" critical analysis
  that motivated this plan).

End.
