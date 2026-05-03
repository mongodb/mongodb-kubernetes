# MC Search MVP to Green — Design Spec

**Date:** 2026-05-03 (updated from 2026-04-30 first cut — phasing reordered to unmanaged-first; scope expanded to full MVP across RS and Sharded)

**Verification target:** Real `$search` *and* `$vectorSearch` queries return correct results from each cluster's local mongot pool in a 2-cluster MC ReplicaSet (delivered at end of Phase 2). Real `$search` queries return correct results across a 2-cluster × 2-shard MC sharded deployment (delivered at end of Phase 3). All single-cluster RS+sharded e2e tests stay green throughout. By end of Phase 5, the full MC Search MVP — RS+Sharded × Q1+Q2 quadrants — is green.

## Phasing reorder rationale (2026-05-03)

The original delivery plan ordered Q1 (managed) before Q2 (external), reasoning that managed is the strategic happy path. We're inverting that for MVP execution:

- **Q2 (unmanaged) is the simpler operator path.** No source CR dereferencing, no automation-config writes, no two-controller race. Operator just reads the `external` block and renders per-cluster mongot ConfigMaps.
- **Q2-RS scaffolding already exists.** The PR #1041 e2e test scaffold is already on the Q2 path and merely needs the operator code to fill in the per-cluster fan-out before assertions can be tightened.
- **Q1 builds on Q2's per-cluster reconcile dimension.** Once the per-cluster mongot ConfigMap renderer ships in Phase 2, Phase 4 (Q1-RS-MC) just adds source CR dereferencing + automation-config writes on top.
- **Sharded scaffolding is heavy and lives in Phase 3.** Per-(cluster, shard) cross-product reconcile + cluster-level Envoy filter chain are net-new and best landed alongside the simpler external-source path before the managed-source variant.

The reordering does NOT change MVP scope (still Phases 1–5 of the delivery plan); only the order of when each quadrant is delivered. The user's named verification target (`$search` + `$vectorSearch` through MC RS) is delivered at end of Phase 2 — earliest possible point.

---

## PR structure

| Layer | What | Targets | Notes |
|-------|------|---------|-------|
| **Base** | Stacked B-section PR train (B1, B14+B18, B16, B3+B4+B13, B5, B8, B9) + new MC E2E harness PR | `search/ga-base` | Foundation everyone needs; existing 7-8 stacked review-decomposed PRs collectively form the base; harness lands as a single new PR after the train. |
| **Phase 2** | Q2-RS-MC operator + tightened MC RS E2E + `$vectorSearch` | `search/ga-base` | One clean PR off ga-base. **Verification target gate G2.** |
| **Phase 3** | Q2-Sh-MC operator + tightened MC sharded E2E | `search/ga-base` | One clean PR off ga-base. Parallel with Phase 2 (independent code paths). |
| **Phase 4** | Q1-RS-MC operator + Q1-RS-MC E2E (KUBE-57) | `search/ga-base` | One clean PR off ga-base. **Sequential after Phase 2** — reuses Phase 2's per-cluster RS mongot renderer. |
| **Phase 5** | Q1-Sh-MC operator + Q1-Sh-MC E2E (KUBE-62) | `search/ga-base` | One clean PR off ga-base. **Sequential after Phase 3** — reuses Phase 3's cross-product + cluster-level Envoy filter chain. |

When all five layers land, `search/ga-base` merges to `master` as a single MVP-done changeset.

### Execution graph

```
        Base (B-train + harness)
        ┌──────┴──────┐
        ▼             ▼
    Phase 2       Phase 3
   (Q2-RS-MC)   (Q2-Sh-MC)
        │             │
        ▼             ▼
    Phase 4       Phase 5
   (Q1-RS-MC)   (Q1-Sh-MC)
```

Phases 2 and 3 are parallel after Base lands. Phases 4 and 5 are parallel after their respective Q2 phases land.

---

## Goal

End the spec with **all five acceptance gates green**, the named verification target — `q2_mc_rs_steady.py` with strict assertions and `$vectorSearch` coverage — delivered at gate G2.

## Scope

### In scope

1. **Base** — land the stacked B-section PR train into `search/ga-base`; build the MC E2E harness (cross-cluster Secret replication + two-cluster fixture lifecycle helpers). Reusable by RS and Sharded MC tests.
2. **Phase 2 (Q2-RS-MC unmanaged)** — operator support for `spec.clusters[i].syncSourceSelector.hosts` on external RS sources; per-cluster mongot fan-out; tightened MC RS e2e (un-skip data plane, restore strict assertions, add `$vectorSearch`).
3. **Phase 3 (Q2-Sh-MC unmanaged)** — operator support for external sharded sources via `spec.source.external.shardedCluster.{router,shards}`; per-(cluster, shard) cross-product reconcile; cluster-level Envoy filter chain (SNI strip-`{shardName}.`); per-shard mongot fan-out per cluster; tightened MC sharded e2e.
4. **Phase 4 (Q1-RS-MC managed)** — recognize `MongoDBMultiCluster` as a search source kind; per-cluster `mongotHost` auto-wire into source CR's automation config (gated on per-cluster Envoy ready); two-controller race fix (OQ-2b); new Q1-RS-MC e2e.
5. **Phase 5 (Q1-Sh-MC managed)** — recognize `MongoDB` MC sharded as a search source kind; mongos `mongotHost` wiring; per-shard mongod `mongotHost` wiring; new Q1-Sh-MC e2e.

### Out of scope (deferred per delivery plan)

- `matchTags` / driver-side `readPreferenceTags` (Phase 7+ polish).
- `replSetConfig` outbound validation, sync-source credentials precondition, distinct failure-mode discrimination (Phase 6+).
- `$vectorSearch` in sharded — RS-only for MVP; per-shard ANN routing is post-MVP.
- Lifecycle hardening — cluster/shard add/remove, churn (Phase 6).
- Top-level conditions, worst-of phase aggregation, DNS soft-check, per-cluster mongod-side endpoint surfacing (Phase 7).
- Auto-embedding leader handover, GA verification suite, telemetry, public docs (Phase 8).

---

## Sub-system decomposition

Five layers; each layer maps to one PR (or one stack for Base). Estimates are coarse working-day sizes.

| Layer | Unit(s) | Depends on | Estimate |
|-------|---------|------------|----------|
| **Base** | B-train merge orchestration; new MC E2E harness PR | nothing | S–M (mostly merge orchestration) + M (1–2d for harness) |
| **Phase 2** | Q2-RS-MC operator (external source per-cluster hosts fan-out); tightened MC RS e2e + `$vectorSearch` | Base | M (2–3d operator + 1d test) |
| **Phase 3** | Q2-Sh-MC operator (cross-product reconcile + cluster-level Envoy filter chain + per-shard hosts fan-out); tightened MC sharded e2e | Base | L (3–5d operator + 1d test) |
| **Phase 4** | Q1-RS-MC operator (MongoDBMulti source kind + mongotHost auto-wire + OQ-2b fix); new Q1-RS-MC e2e | Phase 2 | M–L (2–4d operator + 1d test) |
| **Phase 5** | Q1-Sh-MC operator (MongoDB MC sharded source kind + mongos & mongod mongotHost wiring); new Q1-Sh-MC e2e | Phase 3 | L (4–6d operator + 1–2d test) |

Phases 2 and 3 land in parallel; Phases 4 and 5 land in parallel after their respective Q2 phases.

---

## Architecture

### Component boundaries

**MC E2E harness** lives entirely in `docker/mongodb-kubernetes-tests/` — test code, no operator changes. Owns the cross-cluster Secret replicator (test-pod RBAC, not operator RBAC), two-cluster MongoDBMulti fixture lifecycle, and per-cluster verification helpers. Reused by Phase 2 + Phase 3 + Phase 4 + Phase 5 e2e tests.

**Q2-RS-MC operator** lives in `controllers/searchcontroller/external_search_source.go` and `mongodbsearch_reconcile_helper.go`. The per-cluster reconcile dimension (already scaffolded by B14+B16) iterates `spec.clusters[]`; for external RS sources, when `clusters[i].syncSourceSelector.hosts` is set, the per-cluster mongot ConfigMap renders that cluster's mongot upstream sync-source from those hosts. Falls back to flat `external.hostAndPorts` if `hosts` unset. **No automation-config writes** — Q2 means customer-managed mongods.

**Q2-Sh-MC operator** lives in `controllers/searchcontroller/sharded_external_search_source.go`. Adds per-(cluster, shard) cross-product reconcile: outer loop over `spec.clusters[]`, inner loop over `external.shardedCluster.shards[]`. Each (cluster, shard) pair gets its own mongot StatefulSet + ConfigMap. Per-cluster Envoy gains a per-shard SNI filter chain plus a cluster-level filter chain (derived by stripping `{shardName}.` from `externalHostname`); the cluster-level chain round-robins across all local mongot pools across all shards (used by mongos). **No mongos config writes** for Q2.

**Q1-RS-MC operator** lives in `controllers/searchcontroller/enterprise_search_source.go` (relaxes the `len(clusters) > 1` rejection at `:69-71` for MongoDBMulti sources) and a new `enterprise_multi_search_source.go`. Implements the existing `EnterpriseSearchSource` interface for `MongoDBMultiCluster` source CRs — dereferences `clusterSpecList[]` to expose per-cluster member host lists. The reconciler reuses Phase 2's per-cluster mongot ConfigMap renderer with the source-derived hosts. Adds automation-config writes: per-cluster `mongotHost` (= local Envoy proxy Service FQDN) written into the source CR's `additionalMongodConfig.setParameter`. Gates writes on per-cluster Envoy `Ready`. Includes the OQ-2b two-controller race fix (Envoy controller writes `status.loadBalancer.phase` non-terminally, not just at terminal states).

**Q1-Sh-MC operator** lives in `controllers/searchcontroller/sharded_internal_search_source.go` (already exists in skeleton form per `ls` output). Recognizes `MongoDB` of `kind: ShardedCluster` with multi-cluster topology as a source kind — dereferences shard layout AND cluster layout from the source CR. Reuses Phase 3's cross-product reconcile + cluster-level Envoy filter chain. Adds automation-config writes: mongos cluster-level `mongotHost` block + per-shard mongod `mongotHost` shardOverrides written into the source CR's automation config.

### Data flow — Q2-RS-MC happy path (verification target gate G2)

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
          │   (mongotHost = local Envoy)│                          │   (mongotHost = local Envoy)│
          │   ← test-side fixture        │                          │   ← test-side fixture        │
          │     additionalMongodConfig)  │                          │     additionalMongodConfig)  │
          └─────────────────────────────┘                          └─────────────────────────────┘
                          │                                                         │
                          │  $search / $vectorSearch                                │
                          │  goes mongod → local Envoy → local mongot pool          │
                          ▼                                                         ▼
                       returns rows                                              returns rows
```

### Data flow — Q2-Sh-MC happy path (gate G3)

Cluster-level Envoy filter chain is the new piece:

```
mongos (customer-applied mongotHost = cluster-level proxy svc)
        │
        ▼
Envoy in cluster A (cluster-level filter chain, SNI strip-{shardName}.)
        │
        ├─► round-robin across ALL local mongot pools across ALL shards in cluster A
        │      ├─► mongot-A-shard0-{0,1}
        │      └─► mongot-A-shard1-{0,1}
```

Per-shard mongod (customer-applied mongotHost = per-shard proxy svc) hits the per-shard SNI chain on the same Envoy.

### Data flow — Q1-RS-MC happy path (gate G4)

Identical to Q2-RS-MC except the source is `mongodbResourceRef → MongoDBMulti`, the operator dereferences `clusterSpecList[]` to derive per-cluster member hosts, and the operator writes per-cluster `mongotHost` into the source CR's automation config (test does NOT set `additionalMongodConfig`).

### Data flow — Q1-Sh-MC happy path (gate G5)

Identical to Q2-Sh-MC except source is `mongodbResourceRef → MongoDB` MC sharded, operator dereferences shard+cluster layout, and operator writes mongos cluster-level `mongotHost` + per-shard mongod `mongotHost` shardOverrides into the source's automation config.

### Error handling

| Failure mode | Behavior |
|---|---|
| `clusterName` not registered with operator's MC manager | Reconcile `Failed`, message names the cluster (existing rule from B3+B4+B13). |
| Customer-replicated Secret missing in member cluster | Reconcile `Pending` with B5's per-cluster presence check; message names the missing Secret + cluster. |
| Per-cluster Envoy not ready | `clusterStatusList[i].loadBalancer.phase = Pending`; aggregated phase Pending. For Q1: per-cluster `mongotHost` write deferred until Envoy ready (OQ-2b fix gates this on a non-terminal status write from the Envoy controller). For Q2: no operator gating — customer's mongods may still try to talk to a not-yet-ready Envoy and retry naturally. |
| `clusters[i].syncSourceSelector.hosts` empty | Admission rejects (B3+B4+B13 already validates `hosts` non-empty when `matchTags` absent — see CLARIFY-6). |
| Q1-Sh-MC: `clusterName` not in source CR's `clusterSpecList` | Reconcile `Failed`, fail-fast no auto-align (Phase 2 rule extends to sharded — TD §11.8.1). |
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

**Operator changes** (`controllers/searchcontroller/`):

- `external_search_source.go` — when `spec.clusters[i].syncSourceSelector.hosts` is set, override the flat `external.hostAndPorts` with the per-cluster hosts list as the mongot upstream for cluster `i`'s mongot ConfigMap. Field is per-cluster optional; falls back to flat `hostAndPorts` if unset.
- `mongodbsearch_reconcile_helper.go` — the per-cluster reconcile dimension (B14+B16) extends to external sources; reuse the per-cluster mongot ConfigMap renderer with the external source's per-cluster hosts accessor.
- **No automation-config writes.** Q2 = customer-managed mongods (delivery plan §Phase 5 line 133).

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

### Phase 3 — Q2-Sh-MC (unmanaged)

**Operator changes** (`controllers/searchcontroller/`):

- `sharded_external_search_source.go` — implements per-(cluster, shard) cross-product reconcile for external sharded sources. Reads `spec.source.external.shardedCluster.{router, shards}`. For each `(clusters[i], shards[j])` pair, renders a mongot StatefulSet + ConfigMap with sync-source = `shards[j].hosts` for that shard's cluster slice. Naming follows the cross-product pattern (TD §8 sharded table): `{search}-search-{clusterIndex}-{shardName}-mongot-{podIdx}`.
- `mongodbsearch_reconcile_helper.go` — the per-cluster reconcile gains a sharded inner loop. Topology-agnostic: same code path consumed by Phase 5 (managed).
- **Cluster-level Envoy filter chain** (extends B16) — per-cluster Envoy filter chain config now emits a cluster-level chain (SNI matched against `externalHostname` with `{shardName}.` stripped) plus per-shard chains. The cluster-level chain round-robins across all local mongot pools across all shards.
- **No mongos config writes.** Q2 = customer-applied mongos `mongotHost`.

**Sharded admission rules** (B13 already covers placeholder rules; if `{shardName}.` prefix admission rule is missing, it lands here):

- `externalHostname` must contain `{shardName}` AND (`{clusterName}` or `{clusterIndex}`).
- `externalHostname` must start with `{shardName}.` so the cluster-level form is derivable by stripping the prefix.

**E2E changes** (`docker/mongodb-kubernetes-tests/tests/multicluster_search/q2_mc_sharded_steady.py`):

- Revert iter-5 tolerance changes; restore strict assertions paralleling Phase 2's tightened RS test.
- Un-skip data plane tests; assert `$search` returns rows from every shard.
- Asserts no Envoy SNI collision when stripping `{shardName}.` (Phase 5 ticket DoD; lands here).
- Restore `additionalMongodConfig.mongotHost` on the MongoDBMulti per-shard fixture (customer-applied for Q2).

**Existing single-cluster Q2 sharded regression bar:** `search_sharded_external_mongodb_multi_mongot_unmanaged_lb.py`, `search_sharded_enterprise_external_mongod_managed_lb.py` continue to pass.

**Acceptance (gate G3):** 2-cluster × 2-shard E2E green: `Phase=Running` on MongoDBSearch, per-cluster Envoy ready with both per-shard SNI chains and cluster-level chain functional, `$search` query through customer-applied mongos returns rows from every shard.

### Phase 4 — Q1-RS-MC (managed)

**Depends on:** Phase 2 (per-cluster RS mongot ConfigMap renderer).

**Operator changes** (`controllers/searchcontroller/`):

- `enterprise_search_source.go` — relax the `len(clusters) > 1` gate at `:69-71` to allow `MongoDBMultiCluster` source kinds. Add a source-kind discriminator (`SourceKind` string field on `EnterpriseSearchSource`).
- New `enterprise_multi_search_source.go` — implements the same `EnterpriseSearchSource` interface for `MongoDBMultiCluster` sources. Dereferences `clusterSpecList[]` to expose per-cluster member host lists.
- `mongodbsearch_reconcile_helper.go` — for the multi-cluster source kind, the inner body uses Phase 2's per-cluster mongot ConfigMap renderer with `source.MembersForCluster(clusterName)` as the per-cluster hosts.
- **Per-cluster `mongotHost` automation-config writes** (the new piece in Phase 4): write `mongotHost = "<proxy-svc-FQDN>:<port>"` into the source CR's automation config per cluster member, gated on `Envoy ready in cluster` (B9 status check).
- **Two-controller race fix (OQ-2b):** confirm during kickoff whether the existing Envoy reconciler writes `status.loadBalancer.phase` only at terminal states. If yes, change it to write on every reconcile. The fix lands in this layer.
- `controllers/operator/mongodbsearch_controller.go` — register a watch on `MongoDBMultiCluster` resources for cross-controller event flow; reuses B8's per-member watch infrastructure.

**E2E** — new file `docker/mongodb-kubernetes-tests/tests/multicluster_search/q1_mc_rs_managed.py`:

- Topology: 2-cluster MongoDBMulti RS source with TLS+SCRAM, 3 members per cluster.
- MongoDBSearch with `spec.source.mongodbResourceRef` pointing at the MongoDBMulti (internal source — newly accepted by Phase 4). `spec.clusters[]` with two entries; no `syncSourceSelector` set (hosts derived by operator from `clusterSpecList[]`).
- **Test does NOT set `additionalMongodConfig.mongotHost`** — operator writes it. This is the contract difference vs Phase 2.
- Asserts: MongoDBMulti `Phase=Running`, MongoDBSearch `Phase=Running`, `clusterStatusList` populated and each entry `Running`, per-cluster Envoy ready (`require_ready=True`), `assert_lb_status` consistent, search index reaches `READY`, `$search` aggregation seeded from each member cluster's local pod returns ≥4 expected rows.
- New Evergreen task: `e2e_search_q1_mc_rs_managed`.

**Acceptance (gate G4):** test green with operator-driven `mongotHost` wiring + real `$search` data plane.

### Phase 5 — Q1-Sh-MC (managed)

**Depends on:** Phase 3 (cross-product reconcile + cluster-level Envoy filter chain).

**Operator changes** (`controllers/searchcontroller/`):

- `sharded_internal_search_source.go` — recognizes `MongoDB` of `kind: ShardedCluster` with multi-cluster topology as a source kind. Dereferences shard layout AND cluster layout from the source CR. Reuses Phase 3's cross-product reconcile + cluster-level Envoy filter chain.
- **Cluster-level mongos `mongotHost` block** + **per-shard mongod `mongotHost` shardOverrides** written into the source CR's automation config. Gated on per-cluster Envoy ready.
- TLS secrets keyed per (cluster, shard) (TD §8 wins over §16.3 per OQ-4a).
- Per-cluster `shardOverrides[]` cluster-major placement (inverted vs. `MongoDB.spec.shardOverrides`).
- Reconcile-time `clusterName ↔ clusterIndex` agreement check extends from the Phase 4 RS rule to MC sharded source.

**E2E** — new file `docker/mongodb-kubernetes-tests/tests/multicluster_search/q1_mc_sharded_managed.py`:

- Topology: 2-cluster × 2-shard MongoDB MC sharded source with TLS+SCRAM.
- MongoDBSearch with `spec.source.mongodbResourceRef` pointing at the MongoDB MC sharded source.
- Test does NOT set `additionalMongodConfig.mongotHost` on mongos or per-shard mongods — operator writes it.
- Asserts: source MongoDB `Phase=Running`, MongoDBSearch `Phase=Running`, per-cluster Envoy ready with both per-shard SNI chains and cluster-level chain functional, search index `READY`, `$search` aggregation through mongos returns rows from every shard.
- New Evergreen task: `e2e_search_q1_mc_sharded_managed`.

**Acceptance (gate G5):** test green with operator-driven mongos+mongod `mongotHost` wiring + real `$search` data plane across (cluster, shard) cross-product.

---

## Verification & acceptance gates

| Gate | What's green | When |
|------|--------------|------|
| G1 | Base merged: B-train + harness on `search/ga-base`; existing single-cluster e2es still green; harness smoke test green | End of Base |
| **G2 (named target)** | `q2_mc_rs_steady.py` green with strict assertions, real `$search` + `$vectorSearch` data plane | End of Phase 2 |
| G3 | `q2_mc_sharded_steady.py` green with strict assertions, real `$search` data plane across (cluster, shard) | End of Phase 3 |
| G4 | `q1_mc_rs_managed.py` green with operator-driven `mongotHost` + real `$search` | End of Phase 4 |
| G5 | `q1_mc_sharded_managed.py` green with operator-driven mongos+mongod `mongotHost` + real `$search` | End of Phase 5 |

When G1–G5 are all green, `search/ga-base` is ready to merge into `master` as the MVP-done changeset.

---

## Risks & open items

- **mongot upstream `readPreferenceTags`** may slip past MVP — already mitigated by hosts-first MVP path. No further action.
- **OQ-2b two-controller race** — fix is in scope for Phase 4. Confirm at kickoff whether the existing Envoy reconciler writes `status.loadBalancer.phase` non-terminally; if not, change it.
- **CLARIFY-1** — does the existing per-cluster mongot config rendering (B14/B16) accept a per-cluster `hosts[]` fan-out for external sources, or is that Phase 2's net-new code? Implementer of Phase 2 confirms at kickoff.
- **Voyage API key in CI** — `AI_MONGODB_EMBEDDING_QUERY_KEY` already wired for single-cluster auto-embedding tests; Phase 2 just reuses it. Verify the new MC RS Evergreen task projection includes it.
- **`MongoDBMultiCluster` source CR `clusterSpecList[]` ↔ search CR `clusters[]`** — Phase 4 admission rule: every search-CR `clusterName` must exist in source's `clusterSpecList` and the derived `clusterIndex` must agree (B3+B4+B13). Phase 4 verifies this rule fires correctly on kickoff. Same rule extends to Phase 5 sharded source.
- **Docker image pinning** — mongot version floor check is deferred (Phase 8). MVP assumes all member clusters run a mongot that already supports auto-embedding pod-0 leader. If a cluster runs an older mongot, `$vectorSearch` will fail with a non-friendly error; documented as a known limitation, not a defect.
- **Phase 3 → Phase 5 dependency:** Phase 5 depends on Phase 3's cross-product reconcile + cluster-level Envoy filter chain landing first. If Phase 5 starts before Phase 3 merges, expect substantial rebase churn.
- **Phase 2 → Phase 4 dependency:** lighter — Phase 4 reuses Phase 2's per-cluster mongot ConfigMap renderer but the new code (source kind recognition + automation-config writes) is mostly net-additional.

## Out-of-scope cross-references

- **Phase 6 (lifecycle hardening)**, **Phase 7 (observability polish)**, **Phase 8 (GA readiness + docs)** — separate specs after MVP-done. Spec layouts can mirror this one's "phasing reorder rationale" pattern if execution ordering shifts.
