# log_analyzer

Cross-layer debug-log analyzer for MongoDB Atlas Search on Kubernetes.

When a `$search` query misbehaves you typically need to correlate events across five layers simultaneously: the pymongo client, mongos, per-shard mongod, the envoy LB pod, and mongot. Each layer logs in a different format, uses different identifiers, and lives in a different pod. `log_analyzer` fetches pod logs, parses each layer's format, joins the records by shared cross-layer UUIDs (lsid, cursor\_id, clientId), and renders either a per-cursor tree or a unified timeline.

---

## Package layout

| Module | Owns |
|---|---|
| `analyzer.py` | All parsers (`parse_mongot_log_line`, `parse_mongod_log_line`, `parse_envoy_debug_log`, …), aggregators (`build_stream_summaries`, `read_mongod_sessions`, …), debug-log setup helpers (`set_mongod_debug_logs`, `set_mongos_debug_logs`), and tree/timeline builders. |
| `store.py` | `LogStore` — an in-memory (or on-disk) SQLite database. `load_from_parsed_records()` inserts every layer's output; `build_cursor_trees_sql` / `build_sharded_cursor_trees_sql` / `build_unified_timeline_sql` read it back. |
| `collector.py` | `discover_pods` (label or name-prefix selector) and `fetch_pod_logs` (Python k8s client → local `.log` files). |
| `cli.py` | Standalone `python -m tests.common.search.log_analyzer.cli` — discovers pods, fetches logs, builds trees, prints output. RS and sharded auto-detected. |
| `test_golden.py` | Regression tests against committed fixtures in `tests/common/search/testdata/fixtures/`. |

**Dependency direction:** `collector` → `analyzer` → `store`. `cli` imports all three. Nothing is re-exported from `__init__.py`; import from submodules directly.

**Cross-layer join keys** (all three must be present for a fully stitched tree):

- `lsid` — pymongo logical session id (32-hex). Appears in every COMMAND slow-query record on mongod and mongos.
- `cursor_id` — allocated by mongod (or mongos for the top cursor) on the aggregate reply.
- `clientId` (UUID, 32-hex) — allocated by mongod when it opens a gRPC egress session to envoy. Surfaces in mongod NETWORK:2, the envoy `mongodb-clientid` request header, and the mongot `MongoDbGrpcProtocolInterceptor` DEBUG record.

---

## Enabling debug logging per layer

Missing log verbosity is the most common cause of "the tree shows no lower-layer events". Bump verbosity **before** issuing the queries you want to capture. Always restore it in a `finally` block.

### mongot

mongot logs at `DEBUG` when `spec.logLevel: DEBUG` is set on the `MongoDBSearch` CR. All shipped fixtures and test CRs use this:

```yaml
# docker/mongodb-kubernetes-tests/tests/search/fixtures/search-rs-managed-lb.yaml
spec:
  logLevel: DEBUG
```

The `spec.logLevel` field maps directly to the JVM root log level. There is no separate `MDB_SEARCH_LOG_LEVEL_OVERRIDES` mechanism — it was removed in commit `5b5461104` because it was never wired to any production or test path. `DEBUG` on the CR is the only knob.

The analyzer expects three classes of mongot DEBUG records:

- `io.grpc.netty.NettyServerHandler` — HTTP/2 HEADERS / DATA / RST_STREAM frames per gRPC stream.
- `LuceneSearchBatchProducer` — one record per batch prepared, with `size`, `cursorId`, `clientId`.
- `MongoDbGrpcProtocolInterceptor` — one record per new gRPC stream with `clientId` + gRPC method path. Used as the primary join key to mongod NETWORK. Absent on older mongot builds; the analyzer falls back to time-based correlation in that case.

mongot logs are JSON. Each line is parsed by `parse_mongot_log_line(pod, line)`. Non-JSON lines and lines from unrelated loggers are silently skipped.

### mongod

COMMAND:2 surfaces `$search` aggregate/getMore slow-query records. NETWORK:2 surfaces the gRPC egress session open/close records (log IDs 7401401 / 7401403) carrying the `clientId` UUID — the cross-layer join key.

Helper in `analyzer.py`:

```python
from tests.common.search.log_analyzer.analyzer import set_mongod_debug_logs

# Setup — call BEFORE issuing queries:
set_mongod_debug_logs(mongod_client, command_level=2, network_level=2)

# Teardown — always in finally:
set_mongod_debug_logs(mongod_client, command_level=0, network_level=0)
```

`command_level=2` means "emit every command completion record regardless of `slowOpThresholdMs`". `network_level=2` enables the egress session pair. The helper also bumps `query` verbosity to 1 to reduce plan-cache noise.

For a replica-set test the `mongod_client` is the `MongoClient` already on the `SearchTester`. For sharded tests you need a **direct connection** to each shard primary (see the sharded example below).

### mongos

Same `setParameter` surface as mongod but a separate helper because mongos does not propagate `setParameter` to shards:

```python
from tests.common.search.log_analyzer.analyzer import set_mongos_debug_logs

set_mongos_debug_logs(mongos_client, command_level=2, network_level=2)
# ... queries ...
set_mongos_debug_logs(mongos_client, command_level=0, network_level=0)
```

COMMAND:2 on mongos surfaces `attr.cursorid` (top cursor id), `attr.nShards`, and lsid. NETWORK:2 surfaces `id=4646300` "Sending request" records with the destination `host:port` — used by `read_mongos_remote_requests` to recover per-shard fanout topology when the slow-query record doesn't list `cursor.cursors[]`.

**Sharded tests must call `set_mongos_debug_logs` AND `set_mongod_debug_logs` for each shard's primary.** See the sharded example below for the exact pattern.

### envoy

Two independent log surfaces live on the same envoy pod:

**Always-on stdout JSON access log** — one record per HTTP/2 stream close, emitted unconditionally by the HCM access logger configured in `buildHCMAccessLog`. Contains `upstream_host`, `response_code`, `response_flags`, `grpc_status`, `client_id`, `bytes_received`, `bytes_sent`. Parsed by `read_envoy_access_log` / `parse_envoy_access_log_line`. No admin-endpoint setup required.

**Runtime debug log** (component log) — HTTP/2 frame-level events (HEADERS, DATA, RST_STREAM, stream open/close). Gated behind:

```
kubectl exec <envoy-pod> -- \
  curl -s -X POST localhost:9901/logging?paths=http2:debug,http:debug,router:debug
```

This is opt-in; the test harness does not enable it automatically. The CLI's `--enable-envoy-debug` flag does it for you.

As of commit `3685f981a` the operator runs envoy with `--log-format` so component-log lines are JSON objects with a `message` field. The parser (`_envoy_line_message_and_ts`) extracts the bracketed frame body from `message` and applies the existing regex set. Older fixture captures (plain `[2024-01-02 12:34:56.789][...] ...` text) still parse correctly through the legacy fallback path.

Parsed by `parse_envoy_debug_log(log_paths, namespace=namespace)`. The two surfaces are joined by `merge_envoy_access_log_into_streams(debug_streams, access_entries)` — call it before loading into the store.

### pymongo client wire ops

`SearchConnectivityTool` installs a `_RecordingCommandListener` on its `MongoClient` at construction time. It records every `started` / `succeeded` / `failed` event with `lsid`, `cursor_id`, `server_connection_id`, and timestamps. No setup call is needed.

```python
tool = SearchConnectivityTool(search_tester)
marker = tool.listener.current_marker()

# ... run queries ...

records = tool.listener.snapshot_since(marker)
```

`records` is `list[ClientWireOp]`. Convert to analyzer-side objects with:

```python
from tests.common.search.log_analyzer.analyzer import parse_client_wire_ops

client_ops = parse_client_wire_ops(records, anchor_wall_time=marker_wall)
```

`anchor_wall_time` is a `datetime` representing wall-clock zero for the monotonic timestamps in `ClientWireOp.timestamp`. Pass the `datetime.now(timezone.utc)` captured just before the queries.

See `docker/mongodb-kubernetes-tests/tests/common/search/connectivity.py` for the full `SearchConnectivityTool` and listener implementation.

---

## Working examples

### RS — replica-set paging test

```python
from datetime import datetime, timezone
import time
from tests.common.search.log_analyzer import collector as log_collector
from tests.common.search.log_analyzer.analyzer import (
    build_stream_summaries,
    parse_client_wire_ops,
    parse_envoy_debug_log,
    read_envoy_access_log,
    read_mongod_commands,
    read_mongod_sessions,
    read_mongot_interceptor_events,
    merge_envoy_access_log_into_streams,
    set_mongod_debug_logs,
)
from tests.common.search.log_analyzer.store import LogStore, build_cursor_trees_sql

# 1. Enable debug logging BEFORE issuing queries.
set_mongod_debug_logs(search_tester.client, command_level=2, network_level=2)
marker = datetime.now(timezone.utc)
listener_marker = tool.listener.current_marker()
try:
    tool.paging_search(pages=5, batch_size=10)
    time.sleep(2)   # let slow-query records flush
finally:
    set_mongod_debug_logs(search_tester.client, command_level=0, network_level=0)

# 2. Fetch logs.
since_seconds = int((datetime.now(timezone.utc) - marker).total_seconds()) + 5
mongod_pods = log_collector.discover_pods(namespace, name_prefix=f"{mdb_name}-")
envoy_pods  = log_collector.discover_pods(namespace, label_selector=f"app={envoy_app_label}")
mongot_pods = log_collector.discover_pods(namespace, name_prefix=f"{mdbs_name}-search-")

mongod_logs = log_collector.fetch_pod_logs(namespace, mongod_pods, since_seconds=since_seconds)
envoy_logs  = log_collector.fetch_pod_logs(namespace, envoy_pods,  since_seconds=since_seconds)
mongot_logs = log_collector.fetch_pod_logs(namespace, mongot_pods, since_seconds=since_seconds)

# 3. Parse each layer.
mongod_cmds     = read_mongod_commands(mongod_logs, namespace=namespace)
mongod_sessions = read_mongod_sessions(mongod_logs, namespace=namespace)
envoy_debug     = parse_envoy_debug_log(envoy_logs, namespace=namespace)
envoy_access    = read_envoy_access_log(envoy_logs, namespace=namespace)
envoy_streams   = merge_envoy_access_log_into_streams(envoy_debug, envoy_access)
mongot_streams, mongot_batches = build_stream_summaries(mongot_logs, namespace=namespace)
mongot_opens, mongot_cmds_parsed = read_mongot_interceptor_events(mongot_logs, namespace=namespace)
client_ops = parse_client_wire_ops(
    tool.listener.snapshot_since(listener_marker), anchor_wall_time=marker
)

# 4. Load into the store and build trees.
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
    mongot_cmds=mongot_cmds_parsed,
)
trees = build_cursor_trees_sql(store)
```

### Sharded — per-shard verbosity setup and teardown

The working pattern is in `docker/mongodb-kubernetes-tests/tests/search/search_connectivity_tool_sharded.py`. The key difference from RS: you call `set_mongos_debug_logs` on the mongos client AND call `setParameter` directly on each shard's primary via a direct connection:

```python
from tests.common.search.log_analyzer.analyzer import set_mongos_debug_logs

# Setup
set_mongos_debug_logs(search_tester.client, command_level=2, network_level=2)
for i in range(shard_count):
    t = get_shard_mongod_tester(mdb, shard_index=i, member_index=0,
                                username=admin_user, password=admin_pass)
    try:
        t.client.admin.command(
            "setParameter", 1,
            logComponentVerbosity={"command": {"verbosity": 2}, "network": {"verbosity": 2}},
        )
    finally:
        t.client.close()

# ... queries ...

# Teardown (always in finally)
set_mongos_debug_logs(search_tester.client, command_level=0, network_level=0)
for i in range(shard_count):
    t = get_shard_mongod_tester(mdb, shard_index=i, member_index=0,
                                username=admin_user, password=admin_pass)
    try:
        t.client.admin.command(
            "setParameter", 1,
            logComponentVerbosity={"command": {"verbosity": 0}, "network": {"verbosity": 0}},
        )
    finally:
        t.client.close()
```

Fetch and parse are analogous to the RS case but add `read_mongos_commands` / `read_mongos_remote_requests` and pass `shard_pod_prefixes` to `build_sharded_cursor_trees_sql`:

```python
from tests.common.search.log_analyzer.store import build_sharded_cursor_trees_sql

shard_pod_prefixes = {f"{mdb_name}-{i}": f"{mdb_name}-{i}-" for i in range(shard_count)}
trees = build_sharded_cursor_trees_sql(
    store,
    shard_pod_prefixes=shard_pod_prefixes,
    shard_mongot_pod_prefixes=shard_mongot_pod_prefixes,
)
```

### Capturing a golden fixture

Run from inside the devcontainer with an in-cluster kubeconfig:

```
python -m tests.common.search.testdata.capture_client_wire_ops \
    --scenario rs-paging-clean \
    --namespace ls-25 --mdb-name mdb-rs-conn-tool --topology rs \
    --pages 5 --batch-size 10
```

This writes `<pod>.log` files, `client_wire_ops.jsonl`, and `metadata.json` under `tests/common/search/testdata/fixtures/<scenario>/`. Run `tests/common/search/testdata/generate_golden.py` afterwards to regenerate the `golden/` manifests checked by `test_golden.py`.

### Standalone CLI

```bash
# Live — last 5 minutes, sharded cluster
python -m tests.common.search.log_analyzer.cli \
  --namespace ls-25 --mdbs-name mdb-sh-conn-tool --since 5m

# Replay from a captured snapshot dir
python -m tests.common.search.log_analyzer.cli \
  --logs-dir /tmp/snapshot --mdb-name mdb-sh-conn-tool --mdbs-name mdb-sh-conn-tool

# Ad-hoc SQL against the in-memory store
python -m tests.common.search.log_analyzer.cli \
  --namespace ls-25 --since 5m \
  --sql "SELECT pod, count(*) FROM mongod_commands GROUP BY pod"
```

The CLI does **not** inject pymongo CommandListener events (only live tests have those). It synthesises `ClientWireOp` records from mongod/mongos COMMAND records to drive the existing tree builders.

---

## Multi-cluster (MC) log collection

For MC topologies the mongot pods live on member clusters, not the central cluster. Pass the member cluster's `ApiClient` to `discover_pods` and `fetch_pod_logs`:

```python
pods = log_collector.discover_pods(
    namespace, name_prefix=mongot_prefix, api_client=member_api_client
)
logs = log_collector.fetch_pod_logs(
    namespace, pods, since_seconds=120, api_client=member_api_client
)
```

For pods with multiple containers (e.g. Istio sidecar present), pass `container=` to select the target stream:

```python
logs = log_collector.fetch_pod_logs(
    namespace, pods, since_seconds=120, container="mongot"
)
```

---

## Common pitfalls

**"I see no `COMMAND` records in mongod / mongos."**
Verbosity must be bumped **before** the queries execute. If you call `set_mongod_debug_logs` after opening the cursor the aggregate's slow-query record is already gone. Also check `slowOpThresholdMs`: at verbosity 2 every command is logged regardless of threshold, but if the cluster was restarted between setup and teardown the parameter is lost.

**"Envoy lines fail to parse as JSON."**
Pre-`3685f981a` operator builds emitted component logs as plain bracketed text. The parser handles both formats, but if you see parse warnings the envoy container args likely lack `--log-format`. Ensure the operator image includes the `3685f981a` change. The access log (always-on, `response_code` / `response_flags` records) is unaffected — it was always JSON.

**"mongot has no useful logs / `parse_mongot_log_line` returns nothing."**
`spec.logLevel` on the `MongoDBSearch` CR must be `DEBUG`. Verify with:
```
kubectl get mongodbsearch <name> -o jsonpath='{.spec.logLevel}'
```
Without `DEBUG`, the Netty / interceptor / LuceneSearchBatchProducer records are suppressed at the JVM level and will not appear in the pod log regardless of what the analyzer requests.

**"Per-cluster MC logs are missing from the tree."**
The default `CoreV1Api()` uses the central cluster's API server. Pass `api_client=member_api_client` to both `discover_pods` and `fetch_pod_logs` for every member cluster's pods.

**"Cross-shard fanout records are missing (sharded tree shows only 1 branch)."**
`set_mongos_debug_logs` only affects mongos. Per-shard mongod verbosity must be set separately via a direct connection to each shard's primary. Calling `set_mongos_debug_logs` on the sharded `MongoClient` does **not** propagate `setParameter` to the shard mongods. See the sharded setup example above.

**"mongot batches show but no `clientId` / session join."**
Older mongot builds do not emit the `MongoDbGrpcProtocolInterceptor` record. The analyzer falls back to ±2s time-based correlation between the envoy stream's `opened_at` and mongot stream summaries. The join works but is less precise. If the fixture is from a known-good recent build, confirm the `mongot_stream_opens` table is populated (`store.row_counts()`).
