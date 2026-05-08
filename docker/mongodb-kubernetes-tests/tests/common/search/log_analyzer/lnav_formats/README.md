# lnav formats for log_analyzer captures

lnav format definitions for the four log shapes that
`log_analyzer/collector.py` fetches into `/tmp/pod-logs-*/<pod>.log`.
Drop them into `~/.lnav/formats/installed/` and lnav will recognise the
files by basename pattern, render a unified time-sorted view across all
files you open together (`lnav /tmp/pod-logs-*/`), and expose every
extracted field for SQL via `;SELECT ... FROM <format>` or
`;SELECT ... FROM all_logs`.

## What gets parsed

| Format | File-pattern (basename regex) | Backed by |
|---|---|---|
| `mck_mongod_log` | `(?:mdb-[a-z0-9\-]+-[0-9]+|mdb-sh-.*-(?:mongos|config(?:svr)?)-[0-9]+)\.log$` | mongod + mongos launcher-envelope JSON (`{"logType":"mongodb","contents":"<inner>"}`) |
| `mck_mongot_log` | `mdb-.*-search-(?:[0-9]+|[a-z0-9]+-[a-z0-9]+)\.log$` | mongot logback JSON (`{"t","s","svc","ctx","n","msg",...}`) |
| `mck_envoy_runtime_log` / `mck_envoy_access_log` | `mdb-.*-search-lb-.*\.log$` | envoy stdout — runtime (per-frame JSON) + per-stream access JSON |

Extracted columns surface as SQL columns in the per-format virtual
table. Useful slices:

```sql
-- mongod: find every getMore that hit a real mongot pull failure
;SELECT log_time, ctx, body FROM mck_mongod_log
   WHERE component='COMMAND' AND body LIKE '%errMsg%';

-- mongot: lifecycle events for a single client_id
;SELECT log_time, n AS logger, msg FROM mck_mongot_log
   WHERE n LIKE 'io.grpc%' ORDER BY log_time;

-- envoy access log: distribution of upstream hosts by grpc_status
;SELECT upstream_host, grpc_status, COUNT(*) FROM mck_envoy_access_log
   GROUP BY upstream_host, grpc_status;
```

## Install

```bash
# Host or devc, idempotent
cp tests/common/search/log_analyzer/lnav_formats/*.json \
   ~/.lnav/formats/installed/
```

That's it. Open captures with `lnav <pod>.log [<pod2>.log ...]` and
lnav will pick the matching format per file by `file-pattern`. Use
`:filter-in` / `:filter-out` to narrow on `client_id`, `cursor_id`,
`ctx`, `grpc_status`, etc.

## envoy log shape — why the field is `loc`, not `source`

The operator's envoy `--log-format` template
(`controllers/operator/mongodbsearchenvoy_controller.go`) names the
source-location field **`loc`** rather than `source`. This is
deliberate: lnav 0.13's built-in **JSON graylog demuxer** keys on
`source` + `message`, fires before format detection, and would split
the runtime log into per-source-value piper streams — stripping
timestamp/level/logger from the rendered view. The config knob
`/log/demux-json/graylog/enabled = false` is parsed but ignored at
runtime in lnav 0.13.2. Renaming the field to `loc` sidesteps the
demuxer entirely.

The same template uses `%Y-%m-%dT%H:%M:%S.%e%z` for the time field —
real ISO 8601 with ms + `±HH:MM` offset, so lnav cross-file time-sort
works against mongod/mongot/envoy together. Older captures that
predate this fix carry `"time":"2026"` (literal `%Y` substitution);
lnav still parses the file but falls back to mtime ordering.

## Adding a new layer

When the analyzer starts collecting a new log layer:

1. Capture a representative `<pod>.log` from `/tmp/pod-logs-*/`.
2. Pick a unique field set + file-pattern that distinguishes it from
   the three above (avoid `source`+`message` together — see caveat).
3. Prefer `json: true` when the line is a flat JSON object (lnav
   does timestamp/level extraction for free). Use `regex` only when
   the line embeds JSON-in-a-string (mongod's envelope) or isn't JSON.
4. Validate with `lnav -n <file>` then `lnav -n -c ";SELECT
   COUNT(*) FROM <format>" <file>` — the count must equal the file's
   line count.
