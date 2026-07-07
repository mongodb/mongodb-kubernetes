# Appendix — `search_sharded_routed_from_another_shard` event sequence

Reference companion to `search_sharded_routed_from_another_shard.py`. Documents the
exact sequence of events that makes `TestSearchRoutedFromAnotherShard::test_add_shard`
(variant 1, "natural-flow onboarding") a **zero-outage** proof, and why that result is
sensitive to the mongot version.

## mongot version under test

| | |
|---|---|
| mongot commit | **`72ae26a806`** (bazel build from mongot `master`, staging image) |
| pinned in | `scripts/dev/contexts/variables/mongodb_search_dev` and `scripts/dev/contexts/root-context` |

The pin is **load-bearing for this test** (see "Why it is hitless" below) and must stay
fixed until a released mongot with equivalent initial-sync behavior is available.

## Scenario

Natural-flow onboarding: a new shard is added to the sharded source, its mongot pod
becomes ready and **latches routing-ready while the shard is still empty** (Envoy drops
the `routed_from_another_shard` fallback for that shard), and **only then** is collection
data rebalanced onto it. A back-to-back `$search` prober (oneshot + paging, both
`interval=0`) runs across the whole flow and must see zero `index_unavailable`.

## Observed sequence of events

Representative run (UTC), shard-2 = `mdb-sh-routed-2`, ~71k-doc corpus, ~20k migrated:

| Time (UTC) | Δ | Event | Source |
|---|---|---|---|
| 00:05:25 | — | test scales source 2→3 shards; new shard registers (empty) | test log |
| 00:09:18.369 | — | mongot-2 index gen `a0`: `INITIALIZING → INITIAL_SYNC` (empty shard) | mongot-2 |
| 00:09:18.540 | +0.17s | mongot-2 index gen `a0`: `INITIAL_SYNC → SHUT_DOWN` (nothing to sync) | mongot-2 |
| 00:09:28.173 | — | mongot-2 `CommunityReadinessChecker`: **"Server is ready!"** (`/ready` 200; index `DOES_NOT_EXIST`) | mongot-2 |
| 00:09:29.511 | +1.3s | operator: **`Marked shard "mdb-sh-routed-2" routing-ready: 1 of 1 replicas ready`** — latch drops the Envoy fallback (gated on pod `ReadyReplicas`) | operator |
| 00:09:30 | — | test observes latch; asserts shard owns **0** chunks at latch | test log |
| 00:09:52 | +20s | rebalance START (after the settle floor) — fallback already gone | test log |
| **00:10:05.809** | — | mongot-2 index gen `a1`: `INITIALIZING → INITIAL_SYNC` over migrated data — **outage window OPENS** | mongot-2 |
| **00:10:11.271** | **+5.46s** | mongot-2 index gen `a1`: `INITIAL_SYNC → STEADY_STATE` — **outage window CLOSES** | mongot-2 |
| 00:10:27 | — | rebalance COMPLETE: shard-2 holds **20,658** docs | test log |

**Verdict:** oneshot **1,939 / 1,939** succeeded, paging **88 / 88** succeeded (87 fresh-cursor
reopens) — **0 `index_unavailable`**, across a window that contained the full ~5.5s
INITIAL_SYNC with direct routing active.

## Why it is hitless (and why the pin matters)

A `$search` against a shard mongot is rejected (`IndexUnavailableException`,
`LuceneSearchIndex.throwIfUnavailableForQuerying`) only when the **query-path catalog
status reads `INITIAL_SYNC`**. That catalog status **lags** the replication state machine
by roughly ~7s — during a sync it still reports `DOES_NOT_EXIST`, which is served as empty
results (no error), and only later flips.

- On mongot **`72ae26a806`** the migrated-data INITIAL_SYNC lasts ~**5.5s** — **shorter than
  the catalog lag** — so the query path never observes the rejecting state. It serves
  `DOES_NOT_EXIST` (empty) and then jumps straight to `STEADY`. Result: no outage.
- On older mongot (e.g. `2cb4bc70bc`) the same ~20k-doc sync took ~**67s** — far longer than
  the lag — so the query path *did* observe `INITIAL_SYNC` and `$search` was rejected with
  code 8. That was the original onboarding flake this suite was built to characterize.

So the rejection code path is structurally unchanged across mongot versions; `72ae26a806`
simply syncs migrated data fast enough that the window stays below the observable-rejection
threshold. This is a **mitigation**, not a structural fix. The latch still releases on pod
readiness (~`00:09:29.5`), ~42s before the migrated-data index reaches `STEADY_STATE`.

## Notes

- The complementary delayed-mongot scenario — where the mongot is held *pending* while data
  lands and the Envoy `routed_from_another_shard` fallback absorbs live traffic — is covered
  by `search_sharded_onboarding_no_outage.py`. That exercises the fallback mechanism itself;
  this test exercises the natural latch-on-empty-then-rebalance flow.
- A structural, version-independent fix would gate the routing-ready latch on each shard
  mongot's index `STEADY_STATE` rather than pod readiness. With `72ae26a806` that fix is
  belt-and-suspenders.
- Reproducing the rejection on `72ae26a806` via chunk migration is effectively infeasible:
  a small burst keeps the window below the lag, and a large migration is balancer-throttled
  into an incremental trickle that mongot absorbs in steady-state (no discrete window).
