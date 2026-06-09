# Sharded `$search` availability during shard onboarding — states & error classes

**Date:** 2026-06-09
**Scope:** Single-cluster sharded MongoDB source + MongoDBSearch (managed Envoy LB,
per-shard mongot), MCK operator. Behaviour observed against mongot `master`
@ `2cb4bc70bc3a3aa9821e39ac1c5c037bded93e23`.
**Test:** `docker/mongodb-kubernetes-tests/tests/search/search_sharded_routed_from_another_shard.py`

---

## TL;DR

With today's mongot + operator, **`$search` availability cannot be preserved across a
shard add.** There is an unavoidable window between *data landing on the new shard*
(mongos starts fanning `$search` to it) and *that shard's mongot being queryable*, and
we have no per-index readiness to let the LB route around a still-syncing range.

The achievable guarantee is weaker but precise: make the onboarding gap manifest **only**
as the clean *"no mongot reachable"* failure (`SearchNotEnabled`, then Envoy
*"no healthy upstream"*) and **never** as a mongot **INITIAL_SYNC** rejection (a
Ready-latched mongot serving a still-syncing range — the mode the operator cannot route
around). **Data-first ordering** delivers that guarantee.

A parallel operator branch is implementing transparent rerouting during initial sync
(see [§8](#8-future-behaviour-transparent-reroute-during-initial-sync)); once it lands,
the contract flips to **zero errors** during onboarding. A dormant, skipped test already
encodes that future contract: `search_sharded_onboarding_no_downtime.py`.

---

## 1. The underlying constraint

Two facts about mongot, established from source (full evidence in the companion reports):

- **`/ready` is a sticky, one-way latch.** mongot's readiness gates on *all* indexes
  being in a valid query state (INITIAL_SYNC **excluded**) only for a *brand-new* server.
  Once it first reports Ready it latches (`volatile boolean isReady`, persisted
  `ServerStateEntry.ready`); a new generation/range later entering INITIAL_SYNC does
  **not** flip it back to not-ready.
  → see [`2026-06-09-mongot-ready-readiness-latch.md`](./2026-06-09-mongot-ready-readiness-latch.md)
- **Query-time rejection is independent of `/ready`.**
  `LuceneSearchIndex.throwIfUnavailableForQuerying` rejects a query against an index in
  `UNKNOWN/NOT_STARTED/INITIAL_SYNC/FAILED` regardless of the pod's readiness — so a
  Ready, in-rotation pod can still reject a specific index query with `INITIAL_SYNC`.
- **Migrated data *is* indexed.** mongot's steady-state change stream sets
  `showMigrationEvents(true)`, so chunk-migrated (`fromMigrate=true`) docs flow to the
  mongot and are indexed — there is no fundamental "migration is invisible" gap.
  → see [`2026-06-09-mongot-frommigrate-change-stream.md`](./2026-06-09-mongot-frommigrate-change-stream.md)

The operator wires mongot's `/ready` as the **k8s readiness probe**
(`controllers/searchcontroller/search_construction.go:291-297`). A not-ready pod is
removed from the Service endpoints and therefore from the managed Envoy LB's upstream
set, so the LB returns *"no healthy upstream"* for it.

## 2. The onboarding gap

mongos fans `$search` to a shard the moment that shard **owns chunks** of the collection,
independent of whether the shard's mongot can serve. So once data lands on a freshly
added shard, `$search` needs that shard's mongot — and there is necessarily a window
before the mongot can serve it.

Two orderings, two very different failure shapes:

| Ordering | What happens | Failure during the window |
|---|---|---|
| **empty-mongot-first** (old) | mongot boots on an *empty* shard → indexes trivially STEADY → **latches Ready** → data then migrates in → range enters INITIAL_SYNC behind a Ready, in-rotation pod | **`INITIAL_SYNC` rejection** (bad: LB can't route around it) |
| **data-first** (current test) | data migrated first (no mongot yet) → mongot created *with data present* → genuine initial sync → fresh-server gate keeps it **out of Envoy** until synced | **"no healthy upstream"** only; flips straight to success when Ready |

Data-first never produces an `INITIAL_SYNC` query rejection, because the pod is never
Ready-while-syncing. That is the property the e2e asserts.

## 3. Error-class taxonomy

Classes assigned by `classify_failure()` in
`docker/mongodb-kubernetes-tests/tests/common/search/connectivity.py`:

| Class | Signature (code / message) | Origin | Meaning during onboarding |
|---|---|---|---|
| `search_not_enabled` | code **31082** | shard's mongod | shard owns data but has no `mongotHost` — search not enabled there. **Clean gap.** |
| `transient_network` | `"no healthy upstream"`, connection refused/reset, `NetworkTimeout`/`AutoReconnect`/`ServerSelectionTimeoutError` | Envoy LB / network | no ready mongot upstream behind the route (none deployed, or pod not Ready). **Clean gap.** |
| `mongot_unreachable` | code **50** (`MaxTimeMSExpired`) | mongod fan-out stall | `mongotHost` set but nothing answers (e.g. Envoy not deployed yet) — or, indistinguishably, a reachable mongot that stalls instead of rejecting. **Clean gap** (only an explicit index-state rejection proves the bad mode). |
| `index_unavailable` | message `"…while in state <X>"` (any state token; typically code 8) | **mongot** | query reached a **Ready** mongot, which rejected it because the index isn't servable. **The bad mode — must never occur.** Matched by message, not code — code 8 is a generic error code. |
| `cursor_lost` | code 43 / `CursorNotFound`, `"remote error from mongot"`, `rst_stream` | mongot mid-stream | established cursor lost (relevant to paging tests). |
| `other` | anything else | — | unclassified; tolerated as incidental noise but logged. |

"Clean gap" = the only acceptable onboarding-window failures. `index_unavailable` is a
**tripwire**: its appearance proves the latch trap occurred.

## 4. Onboarding state machine (current behaviour)

Walked one shard forward; each row is a probe verdict against mongos:

| Stage | k8s / config state | `$search` result | Class | e2e assertion |
|---|---|---|---|---|
| baseline | all shards healthy | success | — | `failed == 0` |
| 1 | data on new shard, **no `mongotHost`**, no mongot STS | fail | `search_not_enabled` | `assert_clean_no_mongot_gap` |
| 2 | `mongotHost` set, no Envoy upstream / no mongot | fail | `transient_network` | `assert_clean_no_mongot_gap` |
| 3 | mongot STS exists but **not Ready** (syncing/held) | fail | `transient_network` | `assert_clean_no_mongot_gap` |
| recover | mongot flips Ready (sync complete) | success | — | poll to `failed == 0`, `assert_no_index_unavailable` throughout |

**Invariant across every stage: `index_unavailable == 0`.**

## 5. Distinguishability

- **`transient_network` vs `index_unavailable` — distinguishable.** Different code
  (network/none vs 8), different message (`"no healthy upstream"` vs
  `"while in state INITIAL_SYNC"`), different origin (Envoy vs mongot). The classifier
  checks `index_unavailable` *before* `transient_network`, so a mongot rejection is never
  mislabelled as a clean gap.
- **Stage 2 vs stage 3 — NOT distinguishable from the query error alone.** Both are
  *"no healthy upstream"* because Envoy gates identically on mongot readiness whether the
  STS is absent or merely not-ready. To tell them apart the test inspects k8s state
  (STS existence, `ready_replicas`), not the query error.
- **Consequence:** in a healthy data-first flow a *benign* mongot INITIAL_SYNC error
  cannot be observed — the LB won't forward to a not-ready mongot, so the query never
  reaches mongot until sync is done. Observing `index_unavailable` therefore always
  signals a real defect (the latch trap), which is exactly why it is asserted to be 0.

## 6. What the e2e asserts today

- **Data-first ordering** in `test_add_shard` and in the staged
  `TestShardOnboardingAvailabilityStages`.
- The onboarding gap is the **clean no-mongot error** (`assert_clean_no_mongot_gap`):
  some probes fail, at least one with `transient_network`/`search_not_enabled`, **zero**
  with `index_unavailable`.
- **Recovery** polls back to `failed == 0`, asserting `index_unavailable == 0` the whole
  time.
- **Stale-PVC hygiene:** `delete_mongot_pvcs()` clears a mongot's volume-claim PVCs
  before each onboarded shard's mongot is (re)created, so a recreated mongot can't reuse
  a stale index catalog and latch Ready on it.
- Probes are bounded by `maxTimeMS` so a wedged shard counts as a *failed* probe, never a
  hang.

## 7. Operational gap (GA-relevant)

Because readiness latches and the LB has no per-range awareness, the operator today
cannot shield clients from a freshly-migrated, still-syncing range *if* a mongot ever
becomes Ready before that range finishes syncing. Data-first ordering avoids this in the
managed flow, but the underlying mongot semantics remain a sharp edge for any path that
brings a mongot Ready ahead of its data.

## 8. Future behaviour: transparent reroute during initial sync

A parallel operator branch is adding: **while a newly added shard's mongot is
provisioning / initial-syncing, route `$search` for that shard's slice transparently to
another mongot (or return empty for that slice) so the query returns successfully with no
error.**

**New contract (what the prepared test will assert):**

- Ordering is still **data-first, then mongot**: migrate data onto the new shard, then
  wire `mongotHost` (the induced mongod roll must never interrupt a serving mongot's
  change stream), then create that shard's mongot group.
- Throughout the *entire* onboarding window — shard add, mongot-group creation, pod
  provisioning, and initial sync — **every `$search` succeeds**:
  `failed == 0`, and every failure class (`transient_network`, `search_not_enabled`,
  `index_unavailable`, `cursor_lost`, `other`) is **0**.
- Results may be **empty or reduced** for the still-syncing slice during the window
  (transparent reroute), but the query is never errored.
- After sync completes, full results return.

This is the **inverse** of the current gap assertion: where today we assert the gap fails
*cleanly*, the future test asserts the gap **does not fail at all**.

**Prepared (dormant) test:**
`docker/mongodb-kubernetes-tests/tests/search/search_sharded_onboarding_no_downtime.py`
— module-level `pytest.mark.skip` until the operator feature lands. Activation checklist
is in that file's header.

## 9. References

- Readiness latch report: [`2026-06-09-mongot-ready-readiness-latch.md`](./2026-06-09-mongot-ready-readiness-latch.md)
- `fromMigrate` / change-stream report: [`2026-06-09-mongot-frommigrate-change-stream.md`](./2026-06-09-mongot-frommigrate-change-stream.md)
- Failure classification & helpers: `tests/common/search/connectivity.py`
- Onboarding e2e (current): `tests/search/search_sharded_routed_from_another_shard.py`
