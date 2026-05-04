# MC Search MVP — Later Phases (Sharded + Managed) — Working Notes

**Date:** 2026-05-03
**Status:** WORKING DRAFT — to be re-brainstormed once Phase 2 (Q2-RS-MC) is green. Content carved out of `2026-04-30-rs-mc-mvp-to-green-design.md` to keep the active spec narrowly focused on Base + Phase 2.

> **Scope status (2026-05-04):** Of the phases below, only Phase 3 (Q2-Sh-MC, externally-managed sharded) remains a candidate for *future* MVP expansion under the externally-managed-mongod-only constraint set on this date. **Phase 4 (Q1-RS-MC) and Phase 5 (Q1-Sh-MC) are operator-managed-mongod and therefore explicitly OUT OF MVP.** Treat them as post-MVP design parking only.

This doc is a holding pen for design content covering:

- **Phase 3** — Q2-Sh-MC (unmanaged sharded external)
- **Phase 4** — Q1-RS-MC (managed RS internal) — **POST-MVP / NOT IN MVP (operator-managed mongod)**
- **Phase 5** — Q1-Sh-MC (managed sharded internal) — **POST-MVP / NOT IN MVP (operator-managed mongod)**

The intent: re-brainstorm each phase as its own focused spec when its turn comes (Phase 3 next, then 4 and 5 in parallel). Treat the content below as research input, not as committed design. Anything here may be revised once we have Phase 2 learnings.

---

## Phasing rationale (carry-over)

Phasing order: **Q2 unmanaged → Q1 managed**, in both RS and Sharded lanes.

- **Q2 (unmanaged) is the simpler operator path.** No source CR dereferencing, no automation-config writes, no two-controller race. Operator just reads the `external` block and renders per-cluster mongot ConfigMaps.
- **Q2-RS scaffolding already exists.** PR #1041 e2e test scaffold is already on the Q2 path.
- **Q1 builds on Q2's per-cluster reconcile dimension.** Once the per-cluster mongot ConfigMap renderer ships in Phase 2, Phase 4 just adds source CR dereferencing + automation-config writes on top.
- **Sharded scaffolding is heavy and lives in Phase 3.** Per-(cluster, shard) cross-product reconcile + cluster-level Envoy filter chain are net-new and best landed alongside the simpler external-source path before the managed-source variant.

## Execution graph (full MVP, for reference)

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

Phases 2 + 3 parallel after Base. Phases 4 + 5 parallel after their respective Q2 phases.

---

## Phase 3 — Q2-Sh-MC (unmanaged sharded external)

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

---

## POST-MVP / NOT IN MVP — operator-managed mongod — Phase 4 — Q1-RS-MC (managed RS internal)

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
- **Test does NOT set `additionalMongodConfig.mongotHost`** — operator writes it. Contract difference vs Phase 2.
- Asserts: MongoDBMulti `Phase=Running`, MongoDBSearch `Phase=Running`, `clusterStatusList` populated and each entry `Running`, per-cluster Envoy ready (`require_ready=True`), `assert_lb_status` consistent, search index reaches `READY`, `$search` aggregation seeded from each member cluster's local pod returns ≥4 expected rows.
- New Evergreen task: `e2e_search_q1_mc_rs_managed`.

**Acceptance (gate G4):** test green with operator-driven `mongotHost` wiring + real `$search` data plane.

---

## POST-MVP / NOT IN MVP — operator-managed mongod — Phase 5 — Q1-Sh-MC (managed sharded internal)

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

## Acceptance gates (later phases)

| Gate | What's green | When |
|------|--------------|------|
| G3 | `q2_mc_sharded_steady.py` green with strict assertions, real `$search` data plane across (cluster, shard) | End of Phase 3 |
| G4 | `q1_mc_rs_managed.py` green with operator-driven `mongotHost` + real `$search` | End of Phase 4 |
| G5 | `q1_mc_sharded_managed.py` green with operator-driven mongos+mongod `mongotHost` + real `$search` | End of Phase 5 |

When G3+G4+G5 are all green (after G1+G2 from the active spec), `search/ga-base` is ready to merge into `master` as MVP-done.

---

## Risks & open items (later phases)

- **OQ-2b two-controller race** (Phase 4) — fix is in scope. Confirm at kickoff whether the existing Envoy reconciler writes `status.loadBalancer.phase` non-terminally.
- **`MongoDBMultiCluster` source CR `clusterSpecList[]` ↔ search CR `clusters[]`** — Phase 4 admission rule: every search-CR `clusterName` must exist in source's `clusterSpecList` and the derived `clusterIndex` must agree (B3+B4+B13). Same rule extends to Phase 5 sharded source.
- **Phase 3 → Phase 5 dependency:** Phase 5 depends on Phase 3's cross-product reconcile + cluster-level Envoy filter chain landing first. If Phase 5 starts before Phase 3 merges, expect substantial rebase churn.
- **Phase 2 → Phase 4 dependency:** lighter — Phase 4 reuses Phase 2's per-cluster mongot ConfigMap renderer but the new code (source kind recognition + automation-config writes) is mostly net-additional.
- **Sharded `$vectorSearch`** — explicitly out of MVP. Per-shard ANN routing is non-trivial; revisit post-MVP.
