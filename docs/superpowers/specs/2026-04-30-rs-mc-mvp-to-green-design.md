# RS-MC MVP to Green — Design Spec

**Date:** 2026-04-30
**Verification target:** A 2-cluster `MongoDBSearch` with `len(spec.clusters) > 1` deploys per-cluster mongot fleets, real `$search` *and* `$vectorSearch` queries return correct results from each cluster's local mongot pool, and every RS-related e2e test (single-cluster + multi-cluster) is green.

This is **Spec 1 of 2** for closing out MC Search MVP. Spec 2 (Sharded-MC to green) is a separate brainstorming session that follows.

---

## Goal

End the spec with **`q2_mc_rs_steady.py` green with real-coverage assertions and `$vectorSearch` coverage added**, plus **Q1-RS-MC managed E2E (KUBE-57) green** as the second acceptance gate. All existing single-cluster RS search e2e tests stay green throughout.

## Scope

### In scope

1. **Land the stacked B-section PR train** into `search/ga-base` in dependency order. Today the train is fully coded in worktrees but unmerged.
2. **MC E2E test harness** — cross-cluster Secret replication primitive + two-cluster fixture lifecycle helpers, reusable by RS and (later) sharded MC tests.
3. **Phase 2 operator (Q1-RS-MC managed)** — recognize `MongoDBMultiCluster` as a search source kind; per-cluster reconcile dimension; automatic per-cluster `mongotHost` wiring (hosts-first MVP path; no `readPreferenceTags`); two-controller race fix (OQ-2b).
4. **Phase 3 operator (Q2-RS-MC external)** — accept `spec.clusters[i].syncSourceSelector.hosts` on external RS sources and fan out one mongot fleet per cluster pinned to that cluster's hosts list.
5. **Tightened MC RS E2E** — un-skip data plane; restore `Phase=Running` waits; flip `require_ready=False → True`; restore `additionalMongodConfig.mongotHost` on the MongoDBMulti fixture; add `$vectorSearch` test steps.
6. **Q1-RS-MC E2E (KUBE-57)** — new test that uses `MongoDBMultiCluster` as a *recognized* search source (internal `mongodbResourceRef`), exercising operator-driven `mongotHost` wiring; asserts real `Phase=Running` and `$search` data plane.

### Out of scope (deferred per delivery plan)

- `matchTags` / driver-side `readPreferenceTags` (Phase 7+ polish; mongot upstream may not land).
- `replSetConfig` outbound validation, sync-source credentials precondition, distinct failure-mode discrimination (Phase 6+).
- Lifecycle hardening — cluster add/remove, churn (Phase 6).
- Top-level conditions (`LoadBalancerReady`, `SyncSourceReachable`), worst-of phase aggregation, DNS soft-check, per-cluster mongod-side endpoint surfacing in status (Phase 7).
- Auto-embedding leader handover, GA verification suite, telemetry, public docs (Phase 8).
- Sharded MVP entirely — Phases 4 & 5 (Spec 2).

---

## Sub-system decomposition

Six work units. Units 1–2 unblock everything; Units 3–4 are the operator code; Units 5–6 are the verification gates.

| # | Unit | Depends on | Estimate |
|---|------|------------|----------|
| 1 | Land stacked B-section PR train into `search/ga-base` | nothing | S–M (mostly merge orchestration + rebase fixes) |
| 2 | MC E2E test harness | Unit 1 | M (1–2d) |
| 3 | Phase 2 operator: Q1-RS-MC managed source + per-cluster `mongotHost` auto-wire | Unit 1 | M–L (2–4d) |
| 4 | Phase 3 operator: Q2-RS-MC external source per-cluster hosts fan-out | Unit 1 | S–M (1–2d) |
| 5 | Q1-RS-MC E2E (KUBE-57) — managed source happy path | Units 2 + 3 | M (1–2d) |
| 6 | Tightened Q2-RS-MC E2E + `$vectorSearch` coverage | Units 2 + 4 | S–M (1d) |

Units 3 and 4 can land in either order after Unit 1; they don't share code paths.

---

## Architecture

### Component boundaries

**MC E2E harness** lives entirely in `docker/mongodb-kubernetes-tests/` — test code, no operator changes. Owns the cross-cluster Secret replicator (test-pod RBAC, not operator RBAC), two-cluster fixture lifecycle (deploy MongoDBMulti + wait per-cluster ready), per-cluster verification helpers (resource exists in cluster X, pod ready in cluster X). The replicator copies LB cert + search TLS cert + sync-source CA Secrets from the central cluster to each member cluster after they're created and before the search resource is applied.

**Phase 2 operator** lives in `controllers/searchcontroller/enterprise_search_source.go` (and a new `enterprise_multi_search_source.go` if the multi case is large enough to warrant a separate file — implementer's call). Adds `MongoDBMultiCluster` to the source-resolution switch; the `EnterpriseSearchSource` interface gains a per-cluster member list accessor; `mongodbsearch_reconcile_helper.go` iterates over `spec.clusters[]` and per-cluster derives the local `mongotHost` from `B16`'s per-cluster Envoy proxy Service.

**Phase 3 operator** lives in `controllers/searchcontroller/external_search_source.go`. The internal-source validator at `enterprise_search_source.go:69-71` (which rejects `len(clusters) > 1` for `mongodbResourceRef`) stays — Q2 already uses the external path. The change is in the per-cluster mongot ConfigMap renderer: when `spec.clusters[i].syncSourceSelector.hosts` is set, use those hosts as the cluster's mongot upstream sync-source instead of the flat `external.hostAndPorts`.

**Tightened MC RS E2E** lives at `docker/mongodb-kubernetes-tests/tests/multicluster_search/q2_mc_rs_steady.py`. The relaxed iter-5 commits (`fca043e71`, `afb098a02`) are reverted; iter-3's strict assertions (`058191651`) are restored; new test functions added for `$vectorSearch`.

**Q1-RS-MC E2E** is a new file `docker/mongodb-kubernetes-tests/tests/multicluster_search/q1_mc_rs_managed.py`. Mirrors the Q2 file's structure but with internal `spec.source.mongodbResourceRef` pointing at the MongoDBMulti, and *no* test-side `additionalMongodConfig.mongotHost` (operator does the wiring).

### Data flow — Q2-RS-MC happy path (verification target)

```
                                   ┌─────────────────────────────────────────────┐
                                   │ Central cluster (operator runs here)        │
                                   │                                             │
  customer applies ──────────────► │ MongoDBSearch CR                            │
   MongoDBMulti (RS source)        │   spec.source.external.hostAndPorts: [...]  │
   MongoDBSearch                   │   spec.clusters: [                          │
                                   │     {clusterName: A, replicas: 2,           │
                                   │      syncSourceSelector.hosts:              │
                                   │        ["A-pod-0.A-svc:27017", ...]},       │
                                   │     {clusterName: B, replicas: 2,           │
                                   │      syncSourceSelector.hosts:              │
                                   │        ["B-pod-0.B-svc:27017", ...]}]       │
                                   │                                             │
                                   │ Phase 3 reconciler                          │
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
          │   (mongotHost = local Envoy)│                          │   (mongotHost = local Envoy)│
          └─────────────────────────────┘                          └─────────────────────────────┘
                          │                                                         │
                          │  $search / $vectorSearch                                │
                          │  goes mongod → local Envoy → local mongot pool          │
                          ▼                                                         ▼
                       returns rows                                              returns rows
```

Key invariant: **each member cluster's mongot only indexes the cluster-local `syncSourceSelector.hosts` list**, but every cluster's mongod can reach a fully-indexed result set because $search aggregation routes through the RS primary which can be in any cluster — the mongot pool that handles the query is on the primary's cluster.

### Error handling

| Failure mode | Behavior |
|---|---|
| `clusterName` not registered with operator's MC manager | Reconcile `Failed`, message names the cluster (existing rule from B3+B4+B13). |
| Customer-replicated Secret missing in member cluster | Reconcile `Pending` with B5's per-cluster presence check; message names the missing Secret + cluster. |
| Per-cluster Envoy not ready | `clusterStatusList[i].loadBalancer.phase = Pending`; aggregated phase Pending; mongotHost write deferred until Envoy ready (Phase 2 OQ-2b fix gates this on a non-terminal status write from the Envoy controller). |
| `clusters[i].syncSourceSelector.hosts` empty | Admission rejects (B3+B4+B13 already validates `hosts` non-empty when `matchTags` absent — see CLARIFY-6). |
| Cross-cluster member ↔ Envoy network partition | Out of scope (Phase 6 lifecycle / Phase 7 health checks). |

### Hard-design rules (carry from program)

- **No NetworkPolicy templates**; **no operator-driven Secret replication** (harness does it for tests; customer owns it for prod); **no new RBAC verbs**; **no `EventRecorder.Eventf`**; proxy Service stays `ClusterIP`.

---

## Per-unit details

### Unit 1 — Land stacked B-section PR train

**Today's state** (worktrees verified 2026-04-30):

```
search/ga-base
  └─ #1027 (b1-foundation)         — member-cluster client wiring
      ├─ #1030 (b14-distribution)  — spec.clusters[] + B18 defaulting
      │   ├─ #1036 (b16-envoy-mc)  — per-cluster Envoy
      │   │   └─ #1041 (q2-e2e)    — relaxed test scaffold
      │   ├─ #1034 (b3-b4-b13)     — cluster-index + placeholders + admission
      │   └─ #1033 (b9-status)     — per-cluster status (minimal)
      ├─ #1029 (b5-secrets)        — Secret presence checks
      └─ #1028 (b8-watches)        — per-member-cluster watches
```

**Land order** (matches dependency tree):

1. `#1027` (B1) → `search/ga-base`.
2. `#1030` (B14+B18) → `search/ga-base` after rebase off the new ga-base tip.
3. `#1029` (B5), `#1028` (B8) → `search/ga-base` after rebase. Independent of #1030.
4. `#1036` (B16), `#1034` (B3+B4+B13), `#1033` (B9) → `search/ga-base` after rebase off #1030.
5. `#1041` (Q2 e2e scaffold) — **rebase but DO NOT MERGE YET**. Unit 6 supersedes its assertions; Unit 6's PR replaces this one.

**Out of scope for Unit 1:** code changes. Unit 1 is rebase + merge orchestration only. Test patches required to keep CI green during rebase land in their respective PRs.

**Acceptance:** `git log --oneline master..search/ga-base` shows all eight commits; `make test` (or whatever the unit-test target is) is green on `search/ga-base`; existing single-cluster RS+sharded e2e tests are green on `search/ga-base`.

### Unit 2 — MC E2E test harness

**Files:**

- New: `docker/mongodb-kubernetes-tests/tests/common/multicluster_search/__init__.py`
- New: `docker/mongodb-kubernetes-tests/tests/common/multicluster_search/secret_replicator.py` — copies a Secret from central → all member clusters by name. Idempotent. Used by tests at `setup_method` time after Secrets are created in central.
- New: `docker/mongodb-kubernetes-tests/tests/common/multicluster_search/mc_search_deployment_helper.py` — extends the existing `SearchDeploymentHelper` with `member_cluster_clients` awareness; encapsulates two-cluster MongoDBMulti fixture deployment + per-cluster wait helpers.
- New: `docker/mongodb-kubernetes-tests/tests/common/multicluster_search/per_cluster_assertions.py` — `assert_resource_in_cluster(client, kind, name, namespace)`, `assert_pod_ready_in_cluster(...)`, `assert_envoy_deployment_ready_in_cluster(...)`. The `require_ready` parameter on `assert_envoy_ready_in_each_cluster` defaults to `True` here (the relaxed-test override stays at the call site only).
- Updated: `docker/mongodb-kubernetes-tests/tests/multicluster_search/helpers.py` — re-exports the new helpers; deletes the relaxed-test fallbacks added in iter-5.

**Replication contract:** the Secret replicator runs in test code with the test runner's RBAC. It does NOT rely on operator code paths. Customer-owned Secret replication for prod (delivery plan §Phase 2 / TD §16.5) stays out of scope per program rules.

**Acceptance:** A new harness-only test (`mc_search_harness_smoke.py`) deploys a 2-cluster MongoDBMulti, replicates a fake Secret to both members, asserts presence in each, tears down. Green on Evergreen. Used as the foundation for Units 5 and 6.

### Unit 3 — Phase 2 operator: Q1-RS-MC managed source

**Files:**

- `controllers/searchcontroller/enterprise_search_source.go` — relax the `len(clusters) > 1` gate at `:69-71` to allow `MongoDBMultiCluster` source kinds. Add a source kind discriminator (`SourceKind` string field on `EnterpriseSearchSource`).
- New: `controllers/searchcontroller/enterprise_multi_search_source.go` — implements the same `EnterpriseSearchSource` interface for `MongoDBMultiCluster` sources. Dereferences `clusterSpecList[]` to expose per-cluster member host lists. Hosts-first: no `readPreferenceTags` rendered.
- `controllers/searchcontroller/mongodbsearch_reconcile_helper.go` — the per-cluster reconcile dimension (B14) is already there as scaffolding; this unit fills in the inner body for the multi-cluster source kind. Per cluster: render mongot StatefulSet ConfigMap with `sync-source = source.MembersForCluster(clusterName)`; write `mongotHost = "<proxy-svc-FQDN>:<port>"` into the source CR's automation config gated on `Envoy ready in cluster` (B9 status check).
- `controllers/operator/mongodbsearch_controller.go` — register a watch on `MongoDBMultiCluster` resources for sync (cross-controller event flow); reuses B8's per-member watch infrastructure.

**Two-controller race fix (OQ-2b):** confirm during kickoff whether the existing Envoy reconciler writes `status.loadBalancer.phase` only at terminal states. If yes, change it to write on every reconcile (every 5–15s when not terminal). Without this, the search controller deadlocks waiting for a non-terminal Envoy phase. The fix lands in this unit.

**Out of scope:** mongot version floor check (deferred). `matchTags` rendering (deferred). `replSetConfig` outbound validation (deferred).

**Acceptance:** unit tests (table-driven per source kind) cover: (a) `MongoDBMultiCluster` source resolution, (b) per-cluster member hosts derivation, (c) per-cluster `mongotHost` automation-config writes, (d) Envoy-not-ready gate. Existing single-cluster regression tests still pass.

### Unit 4 — Phase 3 operator: Q2-RS-MC external source

**Files:**

- `controllers/searchcontroller/external_search_source.go` — when `spec.clusters[i].syncSourceSelector.hosts` is set, override the flat `external.hostAndPorts` with the per-cluster hosts list as the mongot upstream for cluster `i`'s mongot ConfigMap. Field is per-cluster optional; falls back to flat `hostAndPorts` if unset.
- `controllers/searchcontroller/mongodbsearch_reconcile_helper.go` — the per-cluster reconcile dimension extends to external sources; reuse the renderer from Unit 3 with the external source's per-cluster hosts accessor.

**No automation-config writes:** Q2 means customer-managed mongods. Operator does NOT write `mongotHost` on external sources; that's customer-applied (delivery plan §Phase 5 line 133, applies to RS too).

**Acceptance:** unit test verifies that with two `clusters[]` entries each pinning different `syncSourceSelector.hosts`, two distinct mongot ConfigMaps are rendered with the correct sync-source values per cluster. Existing single-cluster Q2 RS regression test (`search_replicaset_external_mongodb_multi_mongot_managed_lb.py`) stays green.

### Unit 5 — Q1-RS-MC E2E (KUBE-57)

**Files:**

- New: `docker/mongodb-kubernetes-tests/tests/multicluster_search/q1_mc_rs_managed.py`
- New fixture: `docker/mongodb-kubernetes-tests/tests/multicluster_search/fixtures/search-q1-mc-rs.yaml`
- `.evergreen-tasks.yml` — register `e2e_search_q1_mc_rs_managed` task.

**Topology:**

- 2-cluster MongoDBMulti RS source with TLS+SCRAM. Each cluster has 3 members.
- MongoDBSearch with `spec.source.mongodbResourceRef` pointing at the MongoDBMulti (internal source — newly accepted by Unit 3). `spec.clusters[]` with two entries; no `syncSourceSelector` set (hosts derived by operator from `clusterSpecList[]`).
- Test does NOT set `additionalMongodConfig.mongotHost` on the source — operator writes it.

**Assertions:**

- MongoDBMulti reaches `Phase=Running`.
- MongoDBSearch reaches `Phase=Running` with `clusterStatusList` populated and each entry `Running`.
- Per-cluster Envoy Deployment ready in each member cluster (`require_ready=True`).
- `assert_lb_status` consistent.
- A `default` search index reaches `READY` on `sample_mflix.movies`.
- A `$search` aggregation seeded from each member cluster's local pod returns ≥4 expected rows.
- (No `$vectorSearch` here — that's Unit 6's verification target on the Q2 path.)

**Acceptance:** test green on Evergreen with strict assertions. No `@pytest.mark.skip`.

### Unit 6 — Tightened Q2-RS-MC E2E with `$vectorSearch`

**Files:**

- `docker/mongodb-kubernetes-tests/tests/multicluster_search/q2_mc_rs_steady.py` — revert iter-5 (`fca043e71`) and iter-4 (`afb098a02`) tolerance changes; restore iter-3's (`058191651`) strict assertions:
  - `test_create_search_resource` waits `Phase=Running` (timeout 600s).
  - `test_verify_per_cluster_envoy_deployment` runs with `require_ready=True`.
  - `test_verify_lb_status` asserts `phase=Running`.
  - `test_verify_per_cluster_status` requires `clusterStatusList` populated.
  - `@pytest.mark.skip` markers removed from `test_create_search_index` + `test_execute_text_search_query_per_cluster`.
  - Restore `additionalMongodConfig.setParameter.mongotHost` on the MongoDBMulti source fixture.
- New test functions in the same file:
  - `test_create_vector_search_index` — calls `SampleMoviesSearchHelper.create_auto_embedding_vector_search_index` and waits READY.
  - `test_execute_vector_search_query_per_cluster` — runs `$vectorSearch` from each member cluster's local pod with a Voyage-embedded query string, asserts ≥1 row returned.
- Reuse the existing `SampleMoviesSearchHelper` infrastructure (already supports auto-embedding via `VOYAGE_EMBEDDING_ENDPOINT`).
- Test pulls Voyage API key from the existing `AI_MONGODB_EMBEDDING_QUERY_KEY` env var (used by single-cluster auto-embedding tests).

**Existing single-cluster Q2 RS regression bar:** `search_replicaset_external_mongodb_multi_mongot_managed_lb.py` and `search_replicaset_external_mongodb_multi_mongot_unmanaged_lb.py` continue to pass through this refactor.

**Acceptance:** test green on Evergreen with all strict assertions, plus `$vectorSearch` returning correct ANN matches.

---

## Verification & acceptance gates

| Gate | What's green | When |
|------|--------------|------|
| G1 | All B-section PRs merged into `search/ga-base`; existing single-cluster RS+sharded e2e tests still green on `search/ga-base` | End of Unit 1 |
| G2 | MC E2E harness smoke test green on Evergreen | End of Unit 2 |
| G3 | Phase 2 + Phase 3 operator unit tests green; existing single-cluster Q2 RS regression tests still green | End of Units 3 + 4 |
| **G4 (Q1-RS-MC)** | `q1_mc_rs_managed.py` green on Evergreen with strict assertions, real `$search` data plane | End of Unit 5 |
| **G5 (Q2-RS-MC)** | `q2_mc_rs_steady.py` green on Evergreen with strict assertions, real `$search` + `$vectorSearch` data plane | End of Unit 6 |

G5 is **the verification target the user named**: "real search and vector queries going through in multi-cluster replicaset".

---

## Risks & open items

- **mongot upstream `readPreferenceTags`** may slip past MVP — already mitigated by hosts-first MVP path. No further action.
- **OQ-2b two-controller race** — fix is in scope for Unit 3. Confirm at kickoff whether the existing Envoy reconciler writes `status.loadBalancer.phase` non-terminally; if not, change it.
- **CLARIFY-1** — does the existing per-cluster mongot config rendering (B14/B16) accept a per-cluster `hosts[]` fan-out for external sources, or is that Unit 4's net-new code? Implementer of Unit 4 confirms at kickoff.
- **Voyage API key in CI** — `AI_MONGODB_EMBEDDING_QUERY_KEY` already wired for single-cluster auto-embedding tests; Unit 6 just reuses it. Verify the Evergreen task projection includes it.
- **`MongoDBMultiCluster` source CR `clusterSpecList[]` ↔ search CR `clusters[]`** — Phase 2 admission rule: every search-CR `clusterName` must exist in source's `clusterSpecList` and the derived `clusterIndex` must agree (B3+B4+B13). Unit 3 verifies this rule fires correctly on kickoff.
- **Docker image pinning** — mongot version floor check is deferred (Phase 8). MVP assumes all member clusters run a mongot that already supports auto-embedding pod-0 leader. If a cluster runs an older mongot, `$vectorSearch` will fail with a non-friendly error; documented as a known limitation, not a defect.

## Out-of-scope cross-references

- Spec 2 (Sharded-MC to green) reuses the harness from Unit 2 unchanged. Sharded operator code is independent of Units 3 and 4.
- Phase 6 lifecycle hardening is a separate spec after both MVP specs land.
