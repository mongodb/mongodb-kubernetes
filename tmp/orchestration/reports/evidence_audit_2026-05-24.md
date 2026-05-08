# EVIDENCE.md audit — KUBE-17 connectivity tool test suite

Date: 2026-05-24
Branch: `lsierant/KUBE-17-connectivity-tool`
Baseline HEAD before audit: `0a4402498`
Audit HEAD: `e237e68ed`

Goal: surface every EVIDENCE-vs-assumption gap in the connectivity-tool
test suite before more iteration cycles are wasted on tests built on
unsound premises (as happened with `test_paging_through_mongot_pod_restart_surfaces_lost_cursor`'s
"kill the replacement" mistake).

Commits landed (oldest first):

1. `5e7484721 KUBE-17: sharded disruption tests — one-shot kill + patient drain` (Part 1 + Part 5)
2. `9e20b56ca KUBE-17: drop misleading wire-op-based assertions in test gates` (Part 2 + Part 3)
3. `e84f6b239 KUBE-17: rename MONGOT_PRESTREAM_DRAIN_CEILING → DEFAULT_POST_FAULT_DRAIN_FLOOR` (Part 4)
4. `e237e68ed KUBE-17: paging_baseline_and_fault baseline check — drop wire-op gate` (Part 5)

---

## Part 1 — `repeatedly_hard_kill_pods_in_background` call sites

### Sharded mongot pod-restart test (line 474, original)

What was killed: shard-0 mongot pods (StatefulSet `<mdbs>-search-<mdb>-0-`).
Stated assumption: repeated kills deny "a clean re-serve from the recreated pods" so the cursor
state on the OLD mongot can't be re-served.

Verdict against EVIDENCE: **kill loop is unjustified.**

- EVIDENCE §3.1-3.3: mongot's cursor map is a JVM-heap `ConcurrentHashMap`; the
  replacement pod starts with empty cursor state. One SIGKILL is sufficient to
  drop the OLD pod's state. A replacement pod cannot "re-serve" a cursor that
  never lived in its memory.
- EVIDENCE §3.4-3.5: mongod's task_executor_cursor has no in-place retry for
  `getMore` errors; once mongot returns `CursorNotFound`, the cursor is dead on
  mongod and the next pymongo `next()` surfaces it.
- The test already accepts EITHER outcome (DISRUPTION SURFACED or SEAMLESS RECOVERY)
  — repeated kills cannot change which outcome happens, only mask the post-fault
  drain depth.

Applied fix (commit `5e7484721`):

- One-shot `hard_kill_pods_by_prefix` of shard-0 mongot pods.
- Patient drain: 200 pages @ batch_size=10 with `stop_on_error=True` (was 40
  pages @ batch_size=1 ≈ 40 docs — well below any plausible buffer ceiling).

### Sharded envoy-restart test (line 602, original)

What was killed: envoy Deployment pods (label `app=<mdbs>-search-lb-0`).
Stated assumption: kill-loop is a safety net so a replacement envoy can't
recover the upstream stream.

Verdict against EVIDENCE: **kill loop is redundant.**

- The test scales the envoy Deployment to `replicas=0` BEFORE the kill loop.
- Scale-to-0 prevents the Deployment controller from recreating pods. The
  kill loop only fires while pods are still terminating; once gone, every
  loop iteration is a no-op.
- Per EVIDENCE §9.1-9.3, what matters for the cursor is that:
  (a) envoy's gRPC health check sees `NOT_SERVING` and stops routing new
      streams, and
  (b) existing connections to the OLD envoy survive endpoint removal until
      the upstream TCP closes.
- A single hard-kill (grace=0) accomplishes (b) immediately; with replicas=0
  there's no recreate.

Applied fix (commit `5e7484721`):

- `apps_v1.patch_namespaced_deployment(... replicas=0)` (unchanged).
- Add one `hard_kill_pods_by_label` (grace=0) so envoy stops accepting traffic
  immediately rather than draining for 30s.
- Drop the background kill thread.
- Same patient drain bump as the mongot test.

### Cleanup

`repeatedly_hard_kill_pods_in_background` had only those two call sites; with
both rewritten, the helper is dead code. **Deleted** from
`tests/common/search/connectivity.py`.

---

## Part 2 — Drop pymongo `CommandListener`-based "wire op" metrics

User's directive: "If we don't have a reliable way of gathering [mongot-reach]
then let's drop those counts."

### Decision matrix

The CommandListener captures wire ops between **pymongo and mongod/mongos** —
NOT between mongod and mongot. So:

| Metric / Assertion | Proves… | Doesn't prove… | Action |
|---|---|---|---|
| `result.mongod_wire_ops > 0` (oneshot) | driver crossed pymongo→mongod wire | mongot reached | drop assertion |
| `pages[0].mongod_wire_ops > 0` | firstBatch came from wire | mongot reached | drop assertion (tautological with `pages[0].success`) |
| `verdict.hit_mongod_observed` | ≥1 page touched wire | mongot reached | drop assertion |
| `any(p.success and p.mongod_wire_ops > 0 ...)` (pre-fault sanity) | cursor is live AND went through wire | mongot reached | replace with `any(p.success ...)` |
| `ClientWireOp` dataclass / `wire_ops` field | (capture only) | (n/a) | **keep** — used by `LogStore.build_sharded_cursor_trees_sql` for cross-layer joins via lsid + server_connection_id |
| `_RecordingCommandListener` install | (capture only) | (n/a) | **keep** — diagnostic infrastructure |
| `mongod_wire_ops` field on `QueryResult` | (capture only) | (n/a) | **keep** — used by analyzer pipeline |
| `hit_mongod` / `buffer_only` counters | (capture only) | (n/a) | **keep** — verdict still aggregates them for log lines |

### What "mongot-reach" actually looks like in this codebase

Two helpers exist:

- `assert_search_command_landed_in_cluster` (in
  `tests/common/search/mc_search_helper.py`) — fetches mongot pod logs for a
  window since a marker and asserts a `SearchCommand` interceptor record
  surfaced. Per EVIDENCE: at `spec.logLevel: DEBUG` clientId is captured but
  cursorId is only in TRACE, so the SearchCommand evidence path works but the
  cursorId-distinct-cursor path falls back to `STREAM_OPEN` (per
  `_resolve_cursor_pod_distribution`). The MC tests already use this helper.
- `_resolve_cursor_pod_distribution` (in `tests/search/search_connectivity_tool.py`)
  — same idea, returns a `(mode, pod→count)` map.

For the bare success tests (oneshot / first-page-paging), adding a mongot-log
assertion per call would inflate every test by ~5s of log-fetch overhead AND
introduce time-window flakiness. Per the user's directive, those tests
**drop** the misleading proxy and rely on `result.success` +
`result.returned_count > 0`.

### Call sites migrated (commit `9e20b56ca`)

- `tests/search/search_connectivity_tool.py`:
  - `test_oneshot_search_succeeds_and_reports_upstream` →
    `test_oneshot_search_succeeds` (Part 3 — rename motivated below).
  - `test_paging_search_first_page_is_upstream{,2}` →
    `test_paging_search_first_page_succeeds{,2}`.
  - `test_paging_through_mongot_outage_surfaces_connectivity_error`: pre-fault
    baseline now `any(p.success for p in pre_pages)`.
  - `test_direct_secondary_paging_succeeds`: `any(p.mongod_wire_ops > 0 ...)`
    → `any(p.returned_count > 0 ...)`.
- `tests/multicluster_search/search_connectivity_tool_mc_rs.py`:
  - `test_oneshot_search_through_primary`: drop `mongod_wire_ops>0` +
    `hit_mongod_observed`. Mongot-reach is asserted by the per-cluster direct
    tests (`assert_search_command_landed_in_cluster`).
  - `test_paging_search_first_page_is_upstream` →
    `test_paging_search_first_page_succeeds`.
  - `test_per_cluster_direct_oneshot_search`, `test_per_cluster_direct_paging_search`:
    drop redundant `mongod_wire_ops>0` assertions (already followed by
    `assert_search_command_landed_in_cluster`).
  - `test_paging_through_per_cluster_mongot_outage`: same baseline switch.
- `tests/search/search_connectivity_tool_sharded.py`:
  - `test_paging_search_fans_out_to_distinct_mongot_per_shard`: drop the
    `wire_pages >= 1` assert (already implied by `all(p.success)` for the
    firstBatch).
- `tests/common/search/all_nodes_availability_tool.py`:
  - `NodeAvailabilityResult.oneshot_succeeded`: drop the `mongod_wire_ops>0`
    gate.
  - `NodeAvailabilityResult.paging_succeeded`: drop the `hit_mongod_observed`
    gate.
- `tests/common/search/background_availability_tester.py`:
  - `assert_steady_state`: drop the `require_hit_mongod` parameter.
- `tests/search/search_availability_background_tester.py`:
  - Both scenarios: drop the `verdict.hit_mongod_observed` post-fault
    assertion; replace with `verdict.total > 0`.
- `tests/search/search_failure_modes.py`:
  - Same `verdict.total > 0` substitution.

### What was NOT touched

- `LogStore` SQL schema / `client_wire_ops` table — still consumed by
  `build_sharded_cursor_trees_sql` for the diagnostic cursor-tree render.
- The `mongod_wire_ops` / `wire_ops` fields on `QueryResult`.
- `parse_client_wire_ops` in the analyzer.
- `tool.listener.snapshot_since(...)` — still used by the sharded fanout
  test to feed the LogStore.

These are all observability / diagnostic — not test-pass/fail gates. They
stay so the cursor-tree render keeps working and post-hoc debugging of
e2e runs has the per-call wire history available.

---

## Part 3 — `test_oneshot_search_succeeds_and_reports_upstream`

Original assertions:

```python
assert result.success
assert result.returned_count > 0
assert result.mongod_wire_ops > 0, f"CommandListener not firing..."
assert verdict.hit_mongod_observed
```

The "reports_upstream" claim is unsubstantiated — `mongod_wire_ops > 0` only
proves the driver issued a wire op, not that mongot was reached. Per the
user's directive, the test is **renamed and trimmed** rather than augmented
with a mongot-log fetch (which would add ~5s + flake to a smoke test).

Applied fix (commit `9e20b56ca`):

- Rename `test_oneshot_search_succeeds_and_reports_upstream` →
  `test_oneshot_search_succeeds`.
- Drop the `mongod_wire_ops > 0` + `hit_mongod_observed` assertions.
- Docstring updated to call out that mongot-reach is asserted elsewhere
  (MC suite's `assert_search_command_landed_in_cluster`).

Mongot-reach for the smoke tests is implicitly covered by the steady-state
sample-data test (`SearchSampleDataAndIndexTests.test_smoke_search`) which
must be passing for `returned_count > 0` to hold here, since the smoke
test runs after the search index is built.

---

## Part 4 — `MONGOT_PRESTREAM_DRAIN_CEILING`

The constant's name and value were both misleading:

| Original | Honest? |
|---|---|
| Name: `MONGOT_PRESTREAM_DRAIN_CEILING` | No — it's a CLIENT-SIDE drain floor, not a server cap |
| Value: 3000 | No — empirically the RS pod-restart test needed 50000 (commit `0a4402498`) |
| Comment: "3000 docs exceeds any plausible pre-stream window" | No — empirically wrong |
| Role: `min_post_fault_docs` default | OK as a role |

Applied fix (commit `e84f6b239`):

- Rename `MONGOT_PRESTREAM_DRAIN_CEILING` → `DEFAULT_POST_FAULT_DRAIN_FLOOR`.
- Bump value 3000 → 50000 to match the empirical RS floor.
- Bump `max_post_fault_pages` default 500 → 5000 so the doc floor is reachable
  at typical batch sizes (50000 docs / 10 batch_size = 5000 pages).
- Drop the now-redundant explicit `min_post_fault_docs=50_000` override in
  `test_paging_through_mongot_pod_restart_surfaces_lost_cursor`.
- Updated docstring to describe drain semantics honestly: mongod's
  task_executor_cursor buffer + mongot's server-stream batches stack;
  neither has a known cap, so the only way to guarantee a fresh
  mongod→mongot RPC is to drain past whatever the empirical observed floor is.

---

## Part 5 — Other EVIDENCE-vs-code assumption gaps

### `paging_baseline_and_fault` against EVIDENCE §1 + §3

The function: open paging cursor → read baseline → run fault_fn → drain
post-fault on the SAME cursor → assert either failure surfaced or
min_post_fault_docs reached.

- §1 (one persistent bidi stream per cursor): consistent — the same cursor
  is paged throughout, so the fault kills the stream the cursor is pinned to.
- §3 (mongod task_executor_cursor has no in-place retry): consistent —
  the function relies on the next `getMore` after the fault surfacing the
  failure, no retry expected.

Sub-gap found: the baseline live-cursor check used `p.success and
p.mongod_wire_ops > 0` — a leftover from the Part 2 wire-op-proxy era.
Per EVIDENCE §1, the first paging page IS the cursor's firstBatch which is
a real wire round-trip by construction; `any(p.success ...)` is the
honest live-cursor check.

Applied fix (commit `e237e68ed`):

- Replace the dual-check with `any(p.success for p in baseline)`.
- Comment cites EVIDENCE §1.

### `repeatedly_hard_kill_pods_in_background` itself

Deleted in commit `5e7484721` — no remaining justified call sites (Part 1).

### `test_paging_search_fans_out_to_distinct_mongot_per_shard` (iteration-3 issue)

The static-variant failure mode was already addressed by commit `47e701724`
("guard num_shards=None"). Cross-checked against EVIDENCE §5.1-5.2:

- §5.1: `$search` is `kAnyShard`; generic aggregate routing kicks in.
- §5.2: fanout targets every chunk-owning shard via MinKey..MaxKey traversal.

So the test's accepted evidence shape (`branches ≥ 2` OR `num_shards ≥ 2`)
is correct: per-shard mongod COMMAND records prove fanout at the shard side;
mongos NETWORK 'Sending request' records prove fanout at the routing side;
either is sufficient. Per EVIDENCE §8 the per-shard mongod COMMAND records
can be missing under verbosity timing, which is exactly why the OR shape
exists. No change needed.

### Sharded `test_paging_through_mongot_pod_restart_per_shard_surfaces_lost_cursor`

The "kill the replacement to defeat the cursor" mistake the user called out
in `test_paging_through_mongot_pod_restart_surfaces_lost_cursor` (RS variant)
was inherited here. Same EVIDENCE §3 argument applies — one SIGKILL is
sufficient. Fixed in Part 1 (commit `5e7484721`):

- Drop the kill-loop.
- Drop the redundant `p.mongod_wire_ops > 0` from the drain-pages assertion.

### Sharded `test_paging_through_envoy_restart_surfaces_disruption`

Same fix as above — Part 1.

### MC `test_paging_through_per_cluster_mongot_outage` and
`test_paging_through_per_cluster_envoy_restart`

These already use `paging_baseline_and_fault` with `assert_disruption_observed`
— shapes are consistent with EVIDENCE §1-3 + §9. The only gap was the
pre-fault baseline using the wire-op-proxy idiom, fixed in Part 2.

### `assert_disruption_observed`

Uses `verdict.cursor_lost + verdict.transient_network > 0`. Both buckets are
classified from pymongo error class/message, not wire-op counts. Consistent
with EVIDENCE §3.4 (mongod surfaces `CursorNotFound` / "Remote error from
mongot :: RST_STREAM"). No change.

### `paging_through_mongot_outage_surfaces_connectivity_error`

Opens a NEW cursor after `replicas=0` drained the mongot StatefulSet. Expects
any of `OperationFailure`, `ServerSelectionTimeoutError`, `NetworkTimeout`,
`AutoReconnect`, `ConnectionFailure`. Consistent with EVIDENCE §9.2 (envoy
"no healthy upstream" when all upstreams are gone). The only change here
was the pre-fault baseline switch in Part 2.

### `test_mongot_graceful_shutdown_with_active_cursor_holds_to_grace_and_cursor_dies`

Reviewed against §2 — the time-band assertion `grace - 2 ≤ elapsed ≤ grace + 10`
matches §2.5's "9-of-10 runs exited at 29.6-31.2s" empirical band. No change.

### `test_multi_mongot_sigterm_old_cursor_holds_new_searches_route_away`

Reviewed against §2.5 + §9. The 10-second sleep after deletionTimestamp
matches §9.2 ("envoy probes `grpc.health.v1.Health/Check`" — typical 5s
intervals × 2 to be safe). No change.

### `test_paging_through_envoy_restart_surfaces_lost_cursor` (RS)

Uses `paging_baseline_and_fault` with the default drain budget — now 50000
docs after Part 4. Should now surface the disruption reliably on this
RS-restart path. No further change.

### `test_50_queries_round_robin_distribution_across_mongots` /
`test_single_query_binds_to_single_mongot_with_3_replicas`

Use the `_resolve_cursor_pod_distribution` two-mode resolver. Mode
disambiguation already accounts for the mongot version capability gap
(`SearchCommand` vs `STREAM_OPEN`). Consistent with the EVIDENCE pin
notes ("the operator-shipped MDB_SEARCH_VERSION adds clientId in DEBUG and
cursorId in TRACE"). No change.

---

## Tests / lint status

- `venv/bin/pytest docker/mongodb-kubernetes-tests/kubeobject/` — **27 passed**.
- `venv/bin/pre-commit run --all-files` —
  - `black`, `isort`, `golangci-lint`, `ShellCheck`, etc. all **Passed**.
  - `ty` fails with **only** `unresolved-import` errors (pre-existing
    environment mismatch — venv runs Python 3.13 but ty uses a 3.14
    site-packages cache). All 625 errors are import-resolution; no new
    errors introduced by this audit.
  - `govulncheck` fails with Go version mismatch (package requires go1.26,
    application built with go1.25). Pre-existing; unrelated to this audit.
  - `log_analyzer/test_golden.py` fails at the baseline (committed golden
    out of date with analyzer output); unrelated to this audit. Skipped.

---

## Out-of-scope follow-ups (optional)

- The two-mode `_resolve_cursor_pod_distribution` could be refactored to
  read cursorId from `MongoDbGrpcProtocolInterceptor` DEBUG (clientId is
  present at `logLevel: DEBUG` per EVIDENCE-pin notes), giving per-cursor
  attribution without needing TRACE. Not done — the existing two-mode
  resolver works and the fallback branch is documented.
- The `parse_client_wire_ops` function still exists and is wired through
  the LogStore. If the team decides to drop the wire-op CAPTURE entirely
  later, the LogStore's `build_sharded_cursor_trees_sql` will need an
  alternative cross-layer join key. Out of scope here.
