-- SQLite schema for the cross-layer log analyzer.
--
-- Tables map 1:1 to parser-output dataclasses in log_analyzer/analyzer.py.
-- Timestamps are ISO-8601 TEXT (sort-correct, julianday() for delta).
-- ``raw`` columns hold JSON-shaped slow-query records — queried with
-- json_extract() when needed.

CREATE TABLE client_wire_ops (
    request_id           INTEGER NOT NULL,
    phase                TEXT    NOT NULL,
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

CREATE TABLE mongod_commands (
    rowid_seq            INTEGER PRIMARY KEY AUTOINCREMENT,
    pod                  TEXT    NOT NULL,
    timestamp            TEXT    NOT NULL,
    command              TEXT    NOT NULL,
    namespace            TEXT,
    cursor_id            INTEGER,
    duration_ms          REAL,
    has_search_stage     INTEGER NOT NULL DEFAULT 0,
    lsid                 TEXT,
    server_connection_id INTEGER,
    -- COMMAND "Slow query" record (id=51803) attr fields used to classify
    -- success/failure and detect mongod→mongot calls. ``mongot_request`` is
    -- the attr.mongot JSON blob (cursorid + batchNum + timeWaitingMillis).
    ok                   INTEGER,
    err_msg              TEXT,
    err_name             TEXT,
    err_code             INTEGER,
    mongot_request       TEXT,
    raw                  TEXT
);
CREATE INDEX idx_mc_cursor      ON mongod_commands(cursor_id);
CREATE INDEX idx_mc_lsid_cursor ON mongod_commands(lsid, cursor_id);
CREATE INDEX idx_mc_lsid_cmd    ON mongod_commands(lsid, command);
CREATE INDEX idx_mc_ts          ON mongod_commands(timestamp);

CREATE TABLE mongod_sessions (
    pod                  TEXT    NOT NULL,
    session_id           INTEGER NOT NULL,
    client_id            TEXT    NOT NULL,
    remote               TEXT,
    opened_at            TEXT,
    closed_at            TEXT,
    status               TEXT,
    cursor_id            INTEGER,
    PRIMARY KEY (pod, session_id)
);
CREATE INDEX idx_ms_client      ON mongod_sessions(client_id);
CREATE INDEX idx_ms_cursor      ON mongod_sessions(cursor_id);
CREATE INDEX idx_ms_pod_ts      ON mongod_sessions(pod, opened_at);

CREATE TABLE mongos_commands (
    rowid_seq            INTEGER PRIMARY KEY AUTOINCREMENT,
    pod                  TEXT    NOT NULL,
    timestamp            TEXT    NOT NULL,
    command              TEXT    NOT NULL,
    namespace            TEXT,
    cursor_id            INTEGER,
    duration_ms          REAL,
    has_search_stage     INTEGER NOT NULL DEFAULT 0,
    num_shards           INTEGER,
    shards_targeted      TEXT,
    lsid                 TEXT,
    server_connection_id INTEGER,
    raw                  TEXT
);
CREATE INDEX idx_mongos_cursor  ON mongos_commands(cursor_id);
CREATE INDEX idx_mongos_lsid    ON mongos_commands(lsid);
CREATE INDEX idx_mongos_conn    ON mongos_commands(server_connection_id);
CREATE INDEX idx_mongos_ts      ON mongos_commands(timestamp);

CREATE TABLE mongos_remote_requests (
    pod                  TEXT    NOT NULL,
    timestamp            TEXT    NOT NULL,
    request_id           INTEGER NOT NULL,
    target               TEXT    NOT NULL,
    server_connection_id INTEGER,
    PRIMARY KEY (pod, timestamp, request_id)
);
CREATE INDEX idx_mrr_conn       ON mongos_remote_requests(server_connection_id);
CREATE INDEX idx_mrr_target     ON mongos_remote_requests(target);

CREATE TABLE envoy_streams (
    rowid_seq                 INTEGER PRIMARY KEY AUTOINCREMENT,
    pod                       TEXT    NOT NULL,
    connection_id             INTEGER NOT NULL,
    stream_id                 INTEGER NOT NULL,
    hcm_stream_id             INTEGER,
    path                      TEXT,
    client_id                 TEXT,
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
    from_access_log_only      INTEGER NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX idx_es_natural ON envoy_streams(pod, connection_id, stream_id, client_id);
CREATE INDEX idx_es_client      ON envoy_streams(client_id);
CREATE INDEX idx_es_open        ON envoy_streams(opened_at);

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

CREATE TABLE mongot_batches (
    rowid_seq            INTEGER PRIMARY KEY AUTOINCREMENT,
    pod                  TEXT    NOT NULL,
    timestamp            TEXT    NOT NULL,
    size                 INTEGER,
    cursor_id            INTEGER,
    client_id            TEXT
);
CREATE INDEX idx_mb_pod_ts      ON mongot_batches(pod, timestamp);
CREATE INDEX idx_mb_cursor      ON mongot_batches(cursor_id);
CREATE INDEX idx_mb_client      ON mongot_batches(client_id);

CREATE TABLE mongot_stream_opens (
    rowid_seq            INTEGER PRIMARY KEY AUTOINCREMENT,
    pod                  TEXT    NOT NULL,
    timestamp            TEXT    NOT NULL,
    client_id            TEXT,
    path                 TEXT,
    cursor_id            INTEGER,
    server_connection_id INTEGER
);
CREATE INDEX idx_mso_client     ON mongot_stream_opens(client_id);
CREATE INDEX idx_mso_pod_path   ON mongot_stream_opens(pod, path);

CREATE TABLE mongot_cmds (
    rowid_seq            INTEGER PRIMARY KEY AUTOINCREMENT,
    pod                  TEXT    NOT NULL,
    timestamp            TEXT    NOT NULL,
    command              TEXT,
    client_id            TEXT,
    cursor_id            INTEGER
);
CREATE INDEX idx_mtc_cursor     ON mongot_cmds(cursor_id);
CREATE INDEX idx_mtc_client     ON mongot_cmds(client_id);
