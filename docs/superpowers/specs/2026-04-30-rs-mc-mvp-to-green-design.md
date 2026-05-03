# Q2-RS-MC to Green — Design Spec (Base + Phase 2)

**Date:** 2026-05-03 (updated; scope narrowed to Base + Phase 2 only)

**Verification target:** Real `$search` *and* `$vectorSearch` queries return correct results from each cluster's local mongot pool in a 2-cluster `MongoDBSearch` against an external (unmanaged) `MongoDBMulti` ReplicaSet source. Existing single-cluster RS+sharded e2e tests stay green throughout. Phase 2 is the user's named verification target for MC search MVP.

**Out-of-spec content moved to** [`2026-05-03-mc-mvp-later-phases.md`](./2026-05-03-mc-mvp-later-phases.md): Phase 3 (Q2-Sh-MC unmanaged), Phase 4 (Q1-RS-MC managed), Phase 5 (Q1-Sh-MC managed). That doc is a working draft; each phase will be re-brainstormed as its own focused spec when its turn comes.

---

## Scope

### In scope (this spec)

1. **Base** — land the stacked B-section PR train into `search/ga-base`; build the MC E2E harness (cross-cluster Secret replication + two-cluster fixture lifecycle helpers). Reusable by all MC search e2e tests, not just Phase 2.
2. **Phase 2 (Q2-RS-MC unmanaged)** — operator support for `spec.clusters[i].syncSourceSelector.hosts` on external RS sources; per-cluster mongot fan-out; tightened MC RS e2e (un-skip data plane, restore strict assertions, add `$vectorSearch` coverage).

### Out of scope (deferred)

- **Phases 3, 4, 5** of MC MVP — separate working notes in the later-phases doc; will be re-brainstormed as focused specs after Phase 2 is green.
- `matchTags` / driver-side `readPreferenceTags` — see "Hosts-first MVP routing strategy" below for why hosts is the active path and tag-based is deferred.
- `replSetConfig` outbound validation, sync-source credentials precondition, distinct failure-mode discrimination (Phase 6+).
- `$vectorSearch` in sharded — RS-only for MVP.
- Lifecycle hardening (Phase 6); observability polish (Phase 7); auto-embedding leader handover, GA verification, public docs (Phase 8).

---

## Hosts-first MVP routing strategy (CRITICAL — read before implementing)

The MVP renders per-cluster mongot config with `syncSource.replicaSet.hostAndPort` populated from `spec.clusters[i].syncSourceSelector.hosts`. **This is temporary**; the permanent path is tag-based via `matchTags` + `readPreferenceTags` once mongot supports it upstream.

| Path | Status | Used by |
|------|--------|---------|
| **`syncSourceSelector.hosts`** | **MVP — temporary (seed-only behavior; see below)** | Phase 2 (this spec) |
| `syncSourceSelector.matchTags` + driver-side `readPreferenceTags` | Permanent — post-MVP polish | Future phase 7+ |

### Verified mongot behavior (2026-05-03 agent investigation)

**Critical finding: the hosts list is a seed, NOT an exclusive allowed set.** With concrete file:line citations:

1. **mongot accepts the host list at `syncSource.replicaSet.hostAndPort`** — `mongot/src/main/java/com/xgen/mongot/config/provider/community/MongoConnectionConfig.java:47-52`. Field accepts a list of `host:port` strings; required.

2. **mongot uses the list as a *seed* for standard MongoDB driver topology discovery, not as a static allowed set** — `mongot/src/main/java/com/xgen/mongot/config/provider/community/ConnectionInfoFactory.java:23-54`. The cluster connection string is built with `directConnection=false`, which means the MongoDB driver discovers all RS members from `isMaster/hello` responses and may contact members not on the configured list. The companion comment at `mongot/src/main/java/com/xgen/mongot/util/mongodb/ConnectionStringUtil.java:35-64` explicitly notes: "MMS provides connection strings with `directConnection=true`, which forces the MongoDB driver to connect only to the specified host. This prevents the driver from discovering the primary node in a replica set." So `directConnection=true` is not a viable workaround — it would break primary discovery for writes and linearizable reads.

3. **mongot does NOT yet consume `readPreferenceTags`** — `mongot/src/main/java/com/xgen/mongot/config/provider/community/MongoConnectionConfig.java:73-83`. Only the top-level `readPreference` enum field exists (`PRIMARY`, `SECONDARY_PREFERRED`, etc.). No `readPreferenceTags` or `matchTags`. This confirms the MVP pivot rationale: tag-based routing requires net-new upstream mongot work.

4. **Stale hosts crash mongot at startup** — no graceful fallback. If a host in the configured list isn't reachable / not part of the RS, the MongoDB driver times out during topology discovery and the mongot pod exits.

5. **Operator-side rendering path exists in skeleton** — `mongodb-community-operator/pkg/mongot/mongot_config.go` has `ConfigReplicaSet.HostAndPort []string`. The `SyncSourceSelector` CRD field exists at `api/v1/search/mongodbsearch_types.go:160-172` with both `matchTags` and `hosts`. **The reconciler has not yet been wired to consume `syncSourceSelector.hosts` and populate `ConfigReplicaSet.HostAndPort` per cluster** — that's Phase 2's net-new operator code.

6. **`external.hostAndPorts` becomes redundant for MC mode** — top-level field at `api/v1/search/mongodbsearch_types.go:308-323` is consumed by the single-cluster mongot config rendering path. In MC mode where every cluster has `syncSourceSelector.hosts` populated, the top-level flat list adds no value. Phase 2 admission rule: deprecate top-level `external.hostAndPorts` for `len(clusters) > 1`; require per-cluster `syncSourceSelector.hosts` instead.

### What this means for Phase 2

**The hosts-first MVP path does NOT pin each cluster's mongot to its local RS members.** It seeds the connection with cluster-local hosts; mongot then discovers the full RS topology and syncs from whichever member the driver's `readPreference` selection picks (typically primary or nearest secondary). Cross-cluster sync traffic IS expected and allowed — Istio mesh in test envs (or analogous customer infra in prod) provides the connectivity.

Per-cluster locality in this MVP comes from **per-cluster Envoy proxies routing mongod→mongot traffic locally**, not from constraining mongot's choice of sync source. This is a meaningful split:

- **Per-cluster mongot deployment** (B16) → local search capacity in each cluster ✓
- **Per-cluster Envoy proxy** (B16) → mongod's `mongotHost` resolves locally in each cluster ✓
- **Per-cluster mongot→mongod sync source selection** → NOT enforced by hosts list; uses standard RS topology discovery, may cross clusters ✗

The MVP test suite must reflect this honestly. Asserting "cluster A's mongot syncs only from cluster A's mongods" would be wrong — that's not what hosts-first delivers. The data-plane assertions verify `$search` + `$vectorSearch` returning correct results end-to-end, which works regardless of which cluster's mongod a given mongot syncs from.

**Permanent fix** (post-MVP): when mongot gains `readPreferenceTags` support, the operator switches to rendering `matchTags` per cluster and the driver does proper tag-pinned sync source selection. At that point the hosts seed becomes a documented power-user override rather than the primary mechanism.

---

## PR structure

| Layer | What | Targets | Notes |
|-------|------|---------|-------|
| **Base** | Stacked B-section PR train (B1, B14+B18, B16, B3+B4+B13, B5, B8, B9) + new MC E2E harness PR | `search/ga-base` | Foundation everyone needs (incl. later phases). Existing 7-8 stacked review-decomposed PRs collectively form the base; harness lands as a single new PR after the train. |
| **Phase 2** | Q2-RS-MC operator + tightened MC RS E2E + `$vectorSearch` | `search/ga-base` | One clean PR off ga-base. **Verification target gate G2.** |

When Base + Phase 2 land, the work continues with the later-phases doc. The user's verification target is delivered at end of Phase 2 — earliest possible point.

---

## Sub-system decomposition

| Layer | Unit(s) | Depends on | Estimate |
|-------|---------|------------|----------|
| **Base** | B-train merge orchestration; new MC E2E harness PR | nothing | S–M (mostly merge orchestration) + M (1–2d for harness) |
| **Phase 2** | Q2-RS-MC operator (external source per-cluster hosts fan-out); tightened MC RS e2e + `$vectorSearch` | Base | M (2–3d operator + 1d test) |

---

## Architecture

### Component boundaries

**MC E2E harness** lives entirely in `docker/mongodb-kubernetes-tests/` — test code, no operator changes. Owns the cross-cluster Secret replicator (test-pod RBAC, not operator RBAC), two-cluster MongoDBMulti fixture lifecycle, and per-cluster verification helpers. **Built generic from day one** (not just for Phase 2) — the same helpers will be reused by Phases 3, 4, 5 when those specs land.

**Q2-RS-MC operator** lives in `controllers/searchcontroller/external_search_source.go` and `mongodbsearch_reconcile_helper.go`. The per-cluster reconcile dimension (already scaffolded by B14+B16) iterates `spec.clusters[]`; for external RS sources, when `clusters[i].syncSourceSelector.hosts` is set, the per-cluster mongot ConfigMap renders that cluster's mongot upstream sync-source from those hosts. Falls back to flat `external.hostAndPorts` if `hosts` unset (single-cluster shape). **No automation-config writes** — Q2 means customer-managed mongods (delivery plan §Phase 5 line 133, applies to RS too).

### Data flow — Q2-RS-MC happy path (verification target gate G2)

```
                                   ┌─────────────────────────────────────────────┐
                                   │ Central cluster (operator runs here)        │
                                   │                                             │
  customer applies ──────────────► │ MongoDBSearch CR                            │
   MongoDBMulti (RS source)        │   (no spec.source.external.hostAndPorts —   │
   MongoDBSearch                   │    deprecated for len(clusters) > 1; per-   │
                                   │    cluster syncSourceSelector.hosts is the  │
                                   │    sole mongot-config source)               │
                                   │   spec.clusters: [                          │
                                   │     {clusterName: A, replicas: 2,           │
                                   │      syncSourceSelector.hosts:              │
                                   │        ["A-pod-0.A-svc:27017", ...]},       │
                                   │     {clusterName: B, replicas: 2,           │
                                   │      syncSourceSelector.hosts:              │
                                   │        ["B-pod-0.B-svc:27017", ...]}]       │
                                   │                                             │
                                   │ Phase 2 (Q2-RS-MC) reconciler               │
                                   │   ├─► validates clusterName registration    │
                                   │   ├─► derives clusterIndex (B3 annotation)  │
                                   │   └─► for each cluster i, renders:          │
                                   │         - mongot StatefulSet w/ ConfigMap   │
                                   │           (sync-source = clusters[i].hosts) │
                                   │         - per-cluster Envoy (B16)           │
                                   │           filter chain pointed at local     │
                                   │           mongot pool                       │
                                   └────────────────────┬────────────────────────┘
                                                        │ kube client per cluster
                          ┌────────────────────────────┼────────────────────────────┐
                          ▼                                                         ▼
          ┌─────────────────────────────┐                          ┌─────────────────────────────┐
          │ Member cluster A            │                          │ Member cluster B            │
          │  mongot-A-{0,1}             │                          │  mongot-B-{0,1}             │
          │  Envoy-A (LB cert SAN: A)   │                          │  Envoy-B (LB cert SAN: B)   │
          │  proxy-svc (ClusterIP)      │                          │  proxy-svc (ClusterIP)      │
          │  mongod-A-{0,1,2}           │                          │  mongod-B-{0,1,2}           │
          │   (mongotHost = local Envoy │                          │   (mongotHost = local Envoy │
          │    ← test-side fixture      │                          │    ← test-side fixture      │
          │      additionalMongodConfig)│                          │      additionalMongodConfig)│
          └─────────────────────────────┘                          └─────────────────────────────┘
                          │                                                         │
                          │  $search / $vectorSearch                                │
                          │  goes mongod → local Envoy → local mongot pool          │
                          ▼                                                         ▼
                       returns rows                                              returns rows
```

**What this diagram shows and does NOT show:** the per-cluster Envoy lane (mongod → local Envoy → local mongot pool) IS cluster-local — that's what B16 delivers. The mongot → mongod sync direction (the "fill mongot's index" path, not shown in the diagram) is NOT cluster-local — mongot uses `syncSourceSelector.hosts` as a seed and the MongoDB driver discovers the full RS topology, so a given mongot may pull data from any cluster's mongods. This is acceptable for MVP because `$search` / `$vectorSearch` correctness only requires that *some* mongot has indexed the data, not that each cluster's mongot is locality-pinned. The permanent fix is tag-based routing once mongot supports `readPreferenceTags`.

### Error handling

| Failure mode | Behavior |
|---|---|
| `clusterName` not registered with operator's MC manager | Reconcile `Failed`, message names the cluster (existing rule from B3+B4+B13). |
| Customer-replicated Secret missing in member cluster | Reconcile `Pending` with B5's per-cluster presence check; message names the missing Secret + cluster. |
| Per-cluster Envoy not ready | `clusterStatusList[i].loadBalancer.phase = Pending`; aggregated phase Pending. Q2: no operator gating — customer's mongods may try to talk to a not-yet-ready Envoy and retry naturally. |
| `clusters[i].syncSourceSelector.hosts` empty AND `matchTags` absent | Admission rejects (B3+B4+B13 already validates this — see CLARIFY-6). |
| Cross-cluster member ↔ Envoy network partition | Out of scope (Phase 6 lifecycle / Phase 7 health checks). |

### Hard-design rules (carry from program)

- **No NetworkPolicy templates**; **no operator-driven Secret replication** (harness does it for tests; customer owns it for prod); **no new RBAC verbs**; **no `EventRecorder.Eventf`**; proxy Service stays `ClusterIP`.

---

## Per-layer details

### Base — stacked B-section PR train + MC E2E harness

**B-section train, today's state** (worktrees verified 2026-04-30; user-facing repo state may have moved):

```
search/ga-base
  └─ #1027 (b1-foundation)         — member-cluster client wiring
      ├─ #1030 (b14-distribution)  — spec.clusters[] + B18 defaulting
      │   ├─ #1036 (b16-envoy-mc)  — per-cluster Envoy
      │   ├─ #1034 (b3-b4-b13)     — cluster-index + placeholders + admission
      │   └─ #1033 (b9-status)     — per-cluster status (minimal)
      ├─ #1029 (b5-secrets)        — Secret presence checks
      └─ #1028 (b8-watches)        — per-member-cluster watches
```

**Land order** (matches dependency tree):

1. `#1027` (B1) → `search/ga-base`.
2. `#1030` (B14+B18) → `search/ga-base` after rebase off the new ga-base tip.
3. `#1029` (B5), `#1028` (B8) → `search/ga-base` after rebase. Independent of `#1030`.
4. `#1036` (B16), `#1034` (B3+B4+B13), `#1033` (B9) → `search/ga-base` after rebase off `#1030`.
5. `#1041` (Q2 e2e scaffold) — **DO NOT MERGE.** Phase 2 PR supersedes its assertions; the relaxed test scaffold gets replaced by the tightened version in Phase 2's PR.

**MC E2E harness PR** lands AFTER the B-section train converges on ga-base. New files:

- `docker/mongodb-kubernetes-tests/tests/common/multicluster_search/secret_replicator.py` — copies a Secret from central → all member clusters by name. Idempotent. Used by tests at `setup_method` time after Secrets are created in central.
- `docker/mongodb-kubernetes-tests/tests/common/multicluster_search/mc_search_deployment_helper.py` — extends `SearchDeploymentHelper` with `member_cluster_clients` awareness; encapsulates two-cluster MongoDBMulti fixture deployment + per-cluster wait helpers.
- `docker/mongodb-kubernetes-tests/tests/common/multicluster_search/per_cluster_assertions.py` — `assert_resource_in_cluster(...)`, `assert_pod_ready_in_cluster(...)`, `assert_envoy_deployment_ready_in_cluster(...)`. Default `require_ready=True`; the relaxed-test override stays at the call site only.
- `docker/mongodb-kubernetes-tests/tests/multicluster_search/helpers.py` — re-exports the new helpers; deletes the relaxed-test fallbacks added in iter-5.

**Acceptance (gate G1):** `git log --oneline master..search/ga-base` shows all eight B-train commits + the harness commit; existing single-cluster RS+sharded e2e tests still green; a harness-only smoke test (`mc_search_harness_smoke.py`) deploys a 2-cluster MongoDBMulti, replicates a fake Secret to both members, asserts presence in each, tears down, all green on Evergreen.

### Phase 2 — Q2-RS-MC (unmanaged) — VERIFICATION TARGET

**Operator changes** (`controllers/searchcontroller/` and `mongodb-community-operator/pkg/mongot/`):

- `external_search_source.go` + `mongodbsearch_reconcile_helper.go` — for each entry in `spec.clusters[]`, render that cluster's mongot ConfigMap with `syncSource.replicaSet.hostAndPort` populated from `clusters[i].syncSourceSelector.hosts`. Reuse the existing `ConfigReplicaSet.HostAndPort []string` field at `mongodb-community-operator/pkg/mongot/mongot_config.go`. The per-cluster reconcile dimension is already scaffolded by B14+B16; this unit fills in the external-source code path.
- **CRD admission tightening (chosen path: option b):** for `len(spec.clusters) > 1`, **`spec.source.external.hostAndPorts` is deprecated and ignored**; admission requires every cluster to populate `syncSourceSelector.hosts`. Single-cluster shape (`len(spec.clusters) == 1` or legacy degenerate case) continues to accept the top-level flat list as it does today. This rule lives alongside the B3+B4+B13 admission validators.
- **No automation-config writes.** Q2 = customer-managed mongods (delivery plan §Phase 5 line 133, applies to RS too).

**E2E changes** (`docker/mongodb-kubernetes-tests/tests/multicluster_search/q2_mc_rs_steady.py`):

- Revert iter-5 (`fca043e71`) and iter-4 (`afb098a02`) tolerance changes.
- Restore iter-3's (`058191651`) strict assertions:
  - `test_create_search_resource` waits `Phase=Running` (timeout 600s).
  - `test_verify_per_cluster_envoy_deployment` runs with `require_ready=True`.
  - `test_verify_lb_status` asserts `phase=Running`.
  - `test_verify_per_cluster_status` requires `clusterStatusList` populated.
- Remove `@pytest.mark.skip` markers from `test_create_search_index` + `test_execute_text_search_query_per_cluster`.
- Restore `additionalMongodConfig.setParameter.mongotHost` on the MongoDBMulti fixture (test-side, since Q2 = customer-applied mongotHost).
- New tests:
  - `test_create_vector_search_index` — calls `SampleMoviesSearchHelper.create_auto_embedding_vector_search_index` and waits READY.
  - `test_execute_vector_search_query_per_cluster` — runs `$vectorSearch` from each member cluster's local pod with a Voyage-embedded query string, asserts ≥1 row returned.
- Voyage API key from existing `AI_MONGODB_EMBEDDING_QUERY_KEY` env var (single-cluster auto-embedding tests already use it).

**Existing single-cluster Q2 RS regression bar:** `search_replicaset_external_mongodb_multi_mongot_managed_lb.py` and `search_replicaset_external_mongodb_multi_mongot_unmanaged_lb.py` continue to pass.

**Acceptance (gate G2 — verification target):** test green on Evergreen with all strict assertions, real `Phase=Running` on MongoDBSearch, real per-cluster Envoy `Ready`, real `$search` AND `$vectorSearch` returning correct rows from each member cluster's local pod seed.

---

## Verification & acceptance gates

| Gate | What's green | When |
|------|--------------|------|
| G1 | Base merged: B-train + harness on `search/ga-base`; existing single-cluster e2es still green; harness smoke test green | End of Base |
| **G2 (named target)** | `q2_mc_rs_steady.py` green with strict assertions, real `$search` + `$vectorSearch` data plane | End of Phase 2 |

When G1 + G2 are green, the next iteration (Phase 3 Q2-Sh-MC) starts from the later-phases doc.

---

## Risks & open items

- **Hosts list does NOT enforce per-cluster sync source locality** (resolved 2026-05-03 — agent verification with code citations in "Hosts-first MVP routing strategy"). MVP accepts cross-cluster mongot→mongod sync via standard topology discovery; data-plane correctness holds regardless. Permanent locality fix is post-MVP via mongot `readPreferenceTags` support.
- **`external.hostAndPorts` redundancy in MC mode** (resolved 2026-05-03). Admission rule chosen: option (b) — for `len(clusters) > 1` the top-level flat list is deprecated/ignored; per-cluster `syncSourceSelector.hosts` is required. See Phase 2 "Operator changes" above.
- **Stale hosts crash mongot at startup** (verified 2026-05-03). If the operator renders a hosts list with an unreachable / not-in-RS host, the mongot pod times out during topology discovery and exits. Phase 2 should derive the hosts list deterministically from MongoDBMulti pod-svc FQDNs (which the test fixture already does) — no human typing of hostnames into the CR. For prod, customers populate the hosts list from their RS member list; documenting how to do this safely is a Phase 8 docs item.
- **mongot upstream `readPreferenceTags`** — confirmed not yet implemented (agent verified at `MongoConnectionConfig.java:73-83`). Permanent path requires upstream mongot work. No further action this spec.
- **CLARIFY-1** — does the existing per-cluster mongot config rendering (B14/B16) accept a per-cluster `hosts[]` fan-out for external sources, or is that Phase 2's net-new code? Implementer of Phase 2 confirms at kickoff.
- **Voyage API key in CI** — `AI_MONGODB_EMBEDDING_QUERY_KEY` already wired for single-cluster auto-embedding tests; Phase 2 just reuses it. Verify the new MC RS Evergreen task projection includes it.
- **Docker image pinning** — mongot version floor check is deferred (Phase 8). MVP assumes all member clusters run a mongot that already supports auto-embedding pod-0 leader. If a cluster runs an older mongot, `$vectorSearch` will fail with a non-friendly error; documented as a known limitation, not a defect.

## Cross-references

- [`2026-05-03-mc-mvp-later-phases.md`](./2026-05-03-mc-mvp-later-phases.md) — Phase 3 (Q2-Sh-MC), Phase 4 (Q1-RS-MC), Phase 5 (Q1-Sh-MC) holding pen. Will be re-brainstormed once Phase 2 is green.
