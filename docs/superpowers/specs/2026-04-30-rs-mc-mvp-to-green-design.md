# Q2-RS-MC to Green — Design Spec (Base + Phase 2)

**Date:** 2026-05-03 (updated; scope narrowed to Base + Phase 2 only)

**Verification target:** Real `$search` *and* `$vectorSearch` queries return correct results from each cluster's local mongot pool in a 2-cluster `MongoDBSearch` against an external (unmanaged) `MongoDBMulti` ReplicaSet source. Existing single-cluster RS+sharded e2e tests stay green throughout. Phase 2 is the user's named verification target for MC search MVP.

**Out-of-spec content moved to** [`2026-05-03-mc-mvp-later-phases.md`](./2026-05-03-mc-mvp-later-phases.md): Phase 3 (Q2-Sh-MC unmanaged), Phase 4 (Q1-RS-MC managed), Phase 5 (Q1-Sh-MC managed). That doc is a working draft; each phase will be re-brainstormed as its own focused spec when its turn comes.

---

## Scope

### In scope (this spec)

1. **Base** — land the stacked B-section PR train into `search/ga-base`; build the MC E2E harness (cross-cluster Secret replication + two-cluster fixture lifecycle helpers). Reusable by all MC search e2e tests, not just Phase 2.
2. **Phase 2 (Q2-RS-MC unmanaged)** — operator support for per-cluster mongot fan-out on external RS sources, with every cluster's mongot config seeded from top-level `spec.source.external.hostAndPorts`; tightened MC RS e2e (un-skip data plane, restore strict assertions, add `$vectorSearch` coverage).

### Out of scope (deferred)

- **Phases 3, 4, 5** of MC MVP — separate working notes in the later-phases doc; will be re-brainstormed as focused specs after Phase 2 is green.
- `matchTags` / driver-side `readPreferenceTags` — see "Routing strategy" below for why MVP uses top-level seeds only and tag-based is deferred.
- `replSetConfig` outbound validation, sync-source credentials precondition, distinct failure-mode discrimination (Phase 6+).
- `$vectorSearch` in sharded — RS-only for MVP.
- Lifecycle hardening (Phase 6); observability polish (Phase 7); auto-embedding leader handover, GA verification, public docs (Phase 8).

---

## Routing strategy: top-level seed list in MVP; add tag filtering when mongot supports it

The MVP renders **every** per-cluster mongot config with the same `syncSource.replicaSet.hostAndPort` populated from `spec.source.external.hostAndPorts` (top-level). Per-cluster `syncSourceSelector` is **accepted but ignored** in MVP — it becomes the active per-cluster knob only post-MVP, when mongot supports `readPreferenceTags`.

| Path | Status | Used by |
|------|--------|---------|
| **Top-level `external.hostAndPorts`** (same seed list to every cluster's mongot) | **MVP — active** | Phase 2 (this spec) |
| Top-level `external.hostAndPorts` + per-cluster `syncSourceSelector.matchTags` | Permanent — post-MVP polish (mongot must add `readPreferenceTags` first) | Future phase 7+ |

### Why top-level only (not per-cluster hosts) for MVP

Per the verified mongot behavior below, the host list is a seed for the MongoDB driver's topology discovery, **not** a literal allowed set. With `directConnection=false` (the only viable mode for RS), the driver discovers the full RS topology via `isMaster/hello` regardless of which subset of hosts is in mongot's config. Sync source selection then runs against the (full) discovered topology by `readPreference`.

So:

- **Per-cluster hosts add no routing benefit in MVP.** Driver behaves identically with cluster-local subset or full top-level list.
- **Per-cluster hosts introduce fragility.** If one cluster's mongods are all down at mongot startup, mongot fails to bootstrap (no reachable seed). The top-level list provides cross-cluster seed redundancy.
- **CRD shape stays simple.** When tags arrive, customers add `syncSourceSelector.matchTags` per cluster — no host fields to add or migrate.

### Verified mongot behavior (2026-05-03 agent investigation)

**Critical finding: the hosts list is a seed, NOT an exclusive allowed set.** With concrete file:line citations:

1. **mongot accepts the host list at `syncSource.replicaSet.hostAndPort`** — `mongot/src/main/java/com/xgen/mongot/config/provider/community/MongoConnectionConfig.java:47-52`. Field accepts a list of `host:port` strings; required.

2. **mongot uses the list as a *seed* for standard MongoDB driver topology discovery, not as a static allowed set** — `mongot/src/main/java/com/xgen/mongot/config/provider/community/ConnectionInfoFactory.java:23-54`. The cluster connection string is built with `directConnection=false`, which means the MongoDB driver discovers all RS members from `isMaster/hello` responses and may contact members not on the configured list. The companion comment at `mongot/src/main/java/com/xgen/mongot/util/mongodb/ConnectionStringUtil.java:35-64` explicitly notes: "MMS provides connection strings with `directConnection=true`, which forces the MongoDB driver to connect only to the specified host. This prevents the driver from discovering the primary node in a replica set." So `directConnection=true` is not a viable workaround — it would break primary discovery for writes and linearizable reads.

3. **mongot does NOT yet consume `readPreferenceTags`** — `mongot/src/main/java/com/xgen/mongot/config/provider/community/MongoConnectionConfig.java:73-83`. Only the top-level `readPreference` enum field exists (`PRIMARY`, `SECONDARY_PREFERRED`, etc.). No `readPreferenceTags` or `matchTags`. This confirms the MVP pivot rationale: tag-based routing requires net-new upstream mongot work.

4. **Stale hosts crash mongot at startup** — no graceful fallback. If a host in the configured list isn't reachable / not part of the RS, the MongoDB driver times out during topology discovery and the mongot pod exits.

5. **Operator-side rendering path exists in skeleton** — `mongodb-community-operator/pkg/mongot/mongot_config.go` has `ConfigReplicaSet.HostAndPort []string`. **For MVP, the reconciler is wired to render `ConfigReplicaSet.HostAndPort` from top-level `spec.source.external.hostAndPorts` for every cluster's mongot** — same flat list to every cluster.

6. **`external.hostAndPorts` is the canonical source list field** in both single-cluster and MC modes. Top-level field at `api/v1/search/mongodbsearch_types.go:308-323` is the renderer's source for `ConfigReplicaSet.HostAndPort`. MVP requires it non-empty for `len(clusters) > 1`. Per-cluster `SyncSourceSelector.matchTags` is a forward-compat field in the CRD (B14+B3), not consumed by Phase 2; gets activated post-MVP when mongot adds `readPreferenceTags`.

### What this means for Phase 2

**MVP does not pin each cluster's mongot to its local RS members at all.** Every cluster's mongot config gets the same top-level seed list; the driver discovers the full topology and picks a sync source by `readPreference`. Cross-cluster mongot→mongod sync traffic IS expected and allowed — Istio mesh in test envs (or analogous customer infra in prod) provides the connectivity.

Per-cluster locality in MVP comes only from **per-cluster Envoy proxies routing mongod→mongot traffic locally**, not from constraining mongot's choice of sync source:

- **Per-cluster mongot deployment** (B16) → local search capacity in each cluster ✓
- **Per-cluster Envoy proxy** (B16) → mongod's `mongotHost` resolves locally in each cluster ✓
- **Per-cluster mongot→mongod sync source selection** → not pinned in MVP; standard RS topology discovery, may cross clusters ✗ (acceptable for MVP; permanent fix is `readPreferenceTags` post-MVP)

The MVP test suite must reflect this honestly. Asserting "cluster A's mongot syncs only from cluster A's mongods" would be wrong — that's not what MVP delivers. The data-plane assertions verify `$search` + `$vectorSearch` returning correct results end-to-end, which works regardless of which cluster's mongod a given mongot syncs from.

### Where this is going (post-MVP shape)

Once mongot adds `readPreferenceTags` support upstream, the customer-facing CRD shape gains tag filters per cluster — and that's the only delta:

```yaml
# MVP today
spec:
  source:
    external:
      hostAndPorts: [<full RS member list>]
  clusters:
    - {clusterName: A, replicas: 2}
    - {clusterName: B, replicas: 2}

# Post-MVP (when mongot supports readPreferenceTags) — one additional field per cluster
spec:
  source:
    external:
      hostAndPorts: [<full RS member list>]   # unchanged
  clusters:
    - clusterName: A
      replicas: 2
      syncSourceSelector:                      # net-new in this shape
        matchTags: {region: us-east-1}
    - clusterName: B
      replicas: 2
      syncSourceSelector:
        matchTags: {region: us-west-2}
```

Zero migration: customers add tags when ready; nothing else moves.

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
| **Phase 2** | Q2-RS-MC operator (per-cluster mongot fan-out from top-level `external.hostAndPorts`); tightened MC RS e2e + `$vectorSearch` | Base | M (2–3d operator + 1d test) |

---

## Architecture

### Component boundaries

**MC E2E harness** lives entirely in `docker/mongodb-kubernetes-tests/` — test code, no operator changes. Owns the cross-cluster Secret replicator (test-pod RBAC, not operator RBAC), two-cluster MongoDBMulti fixture lifecycle, and per-cluster verification helpers. **Built generic from day one** (not just for Phase 2) — the same helpers will be reused by Phases 3, 4, 5 when those specs land.

**Q2-RS-MC operator** lives in `controllers/searchcontroller/external_search_source.go` and `mongodbsearch_reconcile_helper.go`. The per-cluster reconcile dimension (already scaffolded by B14+B16) iterates `spec.clusters[]`; for external RS sources, every cluster's mongot ConfigMap renders `syncSource.replicaSet.hostAndPort` from the same top-level `spec.source.external.hostAndPorts`. Per-cluster differentiation is at the ConfigMap-namespace and Envoy-cert level (B16), not in the seed list. **No automation-config writes** — Q2 means customer-managed mongods (delivery plan §Phase 5 line 133, applies to RS too).

### Per-cluster Envoy topology

Envoy is the load balancer that fronts each cluster's local mongot pool and terminates TLS on incoming `mongod → mongot` query traffic. The MC topology fans this out per cluster.

**Per-cluster (one of these in each member cluster):**

| Object | Naming pattern | Source / responsible PR |
|--------|----------------|--------------------------|
| Envoy Deployment | `{search-name}-search-lb-0-{clusterName}` | B16 — `LoadBalancerDeploymentNameForCluster` at `api/v1/search/mongodbsearch_types.go:865-876` |
| Envoy ConfigMap | `{search-name}-search-lb-0-{clusterName}-config` | B16 — `LoadBalancerConfigMapNameForCluster` at `api/v1/search/mongodbsearch_types.go:878-885` |
| Per-cluster mongot StatefulSet (`{N}` mongots per cluster) | `{search-name}-search-{clusterIndex}` | **Phase 2 net-new** — B14+B16 add the per-cluster reconcile dimension and naming helpers, but the StatefulSet creation in `mongodbsearch_reconcile_helper.go` still uses single-cluster `StatefulSetNamespacedName()`; Phase 2 extends the reconcilePlan to one unit per cluster |
| Per-cluster mongot ConfigMap (mongot's own config; carries `syncSource.replicaSet.hostAndPort`) | `{search-name}-search-{clusterIndex}-mongot-config` | **Phase 2 net-new** — same gap as the StatefulSet; Phase 2 renders one per cluster from top-level `external.hostAndPorts` |
| Proxy Service (the `mongotHost` target) | `{search-name}-search-0-proxy-svc` (**same name in every cluster**) | **Phase 2 net-new** — `ProxyServiceNamespacedName()` already exists at `api/v1/search/mongodbsearch_types.go:497`, but the reconciler creates the Service only in the central cluster today (call site at `mongodbsearch_reconcile_helper.go:157`); Phase 2 extends to create the Service in every member cluster |

**Same in every cluster (single resource, replicated to member clusters by the MC E2E harness in tests / by the customer in prod):**

| Object | Naming pattern | Source / responsible PR |
|--------|----------------|--------------------------|
| LB TLS server cert (`Secret`) | `{search-name}-search-lb-0-cert` (or `{prefix}-{name}-search-lb-0-cert` if prefix set) | B16 — `LoadBalancerServerCert` at `api/v1/search/mongodbsearch_types.go:887-898`. **Single cert for all clusters** — works because the proxy-svc FQDN is identical in every cluster (`{name}-search-0-proxy-svc.{ns}.svc.cluster.local`), so one SAN list covers them all. |
| Sync-source TLS CA (`ConfigMap` or `Secret` per `spec.source.external.tls.ca`) | Customer-supplied | B5 — Secret/ConfigMap presence check per cluster; cross-cluster replication is the harness's job in tests. |
| Mongot user password `Secret` (`{search-name}-{username}-password`) | Customer-supplied via `spec.source.passwordSecretRef` | B5 — same presence-check rule; harness replicates in tests. |

**Why the proxy Service can share a name across clusters but the Deployment cannot:**

- Service is `ClusterIP`-scoped — name resolution is per-cluster. When mongod sets `mongotHost = {name}-search-0-proxy-svc.{ns}.svc.cluster.local`, that FQDN resolves to whichever Envoy pods exist *in that cluster's local DNS*. Cluster A resolves to Envoy-A; cluster B resolves to Envoy-B. Same name, distinct backends — no collision because Services are namespaced to their cluster.
- Deployment is a flat-named API object — if every cluster had a Deployment with the same name, MCK's central client couldn't disambiguate which cluster's it's reading. Hence the `-{clusterName}` suffix on the Deployment but NOT on the Service.

This split is what makes the customer-facing `mongotHost` value a single string that "just works" across every cluster — the customer/test fixture sets `additionalMongodConfig.setParameter.mongotHost = {name}-search-0-proxy-svc.{ns}.svc.cluster.local`, and DNS does the per-cluster routing automatically.

**Envoy filter chain (B16):**

Each cluster's Envoy config (rendered by `controllers/operator/mongodbsearchenvoy_controller.go` per-cluster) carries:

- One TLS listener on the proxy port (`27028` by default).
- SNI filter chain match: `server_names: [{name}-search-0-proxy-svc.{ns}.svc.cluster.local]`. mongod opens TLS to `mongotHost` with that SNI; Envoy matches the chain.
- Cluster definition pointing to local mongot pool (`{search-name}-search-{clusterIndex}` headless Service in the same member cluster).
- A "cluster ID" tag baked into the cluster name so per-cluster filter chains stay distinct in metrics / config (commit `e574935a8`).

**Cluster-local mongod → mongot data path (the customer-facing query lane):**

```
mongod-{cluster A}                    mongod-{cluster B}
   │ TLS, SNI={proxy-svc FQDN}            │ TLS, SNI={proxy-svc FQDN}
   ▼                                       ▼
proxy-svc (in cluster A)              proxy-svc (in cluster B)
   │ ClusterIP DNS → local pods           │ ClusterIP DNS → local pods
   ▼                                       ▼
Envoy-A pods                          Envoy-B pods
(LoadBalancerDeploymentNameForCluster)
   │ matches SNI to filter chain          │ matches SNI to filter chain
   │ → routes to local mongot cluster def │ → routes to local mongot cluster def
   ▼                                       ▼
mongot-A pods                         mongot-B pods
   (StatefulSet in cluster A)            (StatefulSet in cluster B)
```

This is the path B16 + Phase 2 together deliver. Cluster-local end-to-end. Cross-cluster `mongot → mongod` sync direction (the indexing path, not shown) is a different lane and not cluster-local in MVP — see "Routing strategy" earlier in the doc.

### Data flow — Q2-RS-MC happy path (verification target gate G2)

```
                                   ┌─────────────────────────────────────────────┐
                                   │ Central cluster (operator runs here)        │
                                   │                                             │
  customer applies ──────────────► │ MongoDBSearch CR                            │
   MongoDBMulti (RS source)        │   spec.source.external.hostAndPorts:        │
   MongoDBSearch                   │     [<full RS member list>]                 │
                                   │     (active mongot-config rendering source) │
                                   │   spec.clusters: [                          │
                                   │     {clusterName: A, replicas: 2},          │
                                   │     {clusterName: B, replicas: 2}]          │
                                   │                                             │
                                   │ Phase 2 (Q2-RS-MC) reconciler               │
                                   │   ├─► validates clusterName registration    │
                                   │   ├─► derives clusterIndex (B3 annotation)  │
                                   │   └─► for each cluster i, renders:          │
                                   │         - mongot StatefulSet w/ ConfigMap   │
                                   │           (sync-source = top-level          │
                                   │            external.hostAndPorts;           │
                                   │            same list to every cluster)      │
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

**What this diagram shows and does NOT show:** the per-cluster Envoy lane (mongod → local Envoy → local mongot pool) IS cluster-local — that's what B16 delivers. The mongot → mongod sync direction (the "fill mongot's index" path, not shown in the diagram) is NOT cluster-local — every cluster's mongot is seeded with the same top-level host list, and the MongoDB driver discovers the full RS topology, so a given mongot may pull data from any cluster's mongods. This is acceptable for MVP because `$search` / `$vectorSearch` correctness only requires that *some* mongot has indexed the data, not that each cluster's mongot is locality-pinned. The permanent fix is tag-based routing once mongot supports `readPreferenceTags`.

### Error handling

| Failure mode | Behavior |
|---|---|
| `clusterName` not registered with operator's MC manager | Reconcile `Failed`, message names the cluster (existing rule from B3+B4+B13). |
| Customer-replicated Secret missing in member cluster | Reconcile `Pending` with B5's per-cluster presence check; message names the missing Secret + cluster. |
| Per-cluster Envoy not ready | `clusterStatusList[i].loadBalancer.phase = Pending`; aggregated phase Pending. Q2: no operator gating — customer's mongods may try to talk to a not-yet-ready Envoy and retry naturally. |
| `external.hostAndPorts` empty for `len(clusters) > 1` | Admission rejects: top-level field is the canonical source list for MC mode. |
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

- `external_search_source.go` + `mongodbsearch_reconcile_helper.go` — for each entry in `spec.clusters[]`, render that cluster's mongot ConfigMap with `syncSource.replicaSet.hostAndPort` populated from **top-level `spec.source.external.hostAndPorts`** (same list to every cluster's mongot). Reuse the existing `ConfigReplicaSet.HostAndPort []string` field at `mongodb-community-operator/pkg/mongot/mongot_config.go`. The per-cluster reconcile dimension is already scaffolded by B14+B16; this unit fills in the external-source code path. **No per-cluster hosts plumbing** — the rendering source is identical across clusters.
- **CRD admission rule (`len(spec.clusters) > 1`):** `spec.source.external.hostAndPorts` is required (non-empty). `spec.clusters[i].syncSourceSelector.matchTags` is accepted but not consumed by Phase 2 — the field exists in the CRD already (B14+B3 deliverables) and gets activated post-MVP when mongot supports `readPreferenceTags`. Today, customers leaving it empty is the canonical MC shape. Customers populating `matchTags` today get a no-op + (optional) warning event noting the field is reserved for post-MVP use.
- **Single-cluster shape unchanged.** `len(spec.clusters) ≤ 1` continues to render mongot config from top-level `external.hostAndPorts` exactly as it does today.
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
- **Drop per-cluster `syncSourceSelector.hosts` from the CR fixture** — these were added in iter-1 (`fc5575b31`) under the assumption that mongot would honor them as a literal allowed set; agent verification disproved that. Phase 2's CR shape is just `clusters: [{clusterName, replicas}, ...]` with the source list at top-level `external.hostAndPorts`.
- **Drop the `REGION_TAGS` pinning** on the MongoDBMulti source — the `memberConfig[].tags.region` annotations only mattered if mongot was going to consume `readPreferenceTags`. Mongot doesn't yet, and we're not setting `matchTags` either, so the tags are unused.
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
- **`external.hostAndPorts` role in MC mode** (resolved 2026-05-03). The top-level field is the canonical source list and the active mongot-config rendering source for both single-cluster and MC modes. Phase 2 admission requires it non-empty for `len(clusters) > 1`. Per-cluster `syncSourceSelector.matchTags` is a forward-compat field in the CRD (B14+B3), not consumed by Phase 2; gets activated post-MVP when mongot adds `readPreferenceTags`. See Phase 2 "Operator changes" and "Where this is going" sections above.
- **Stale hosts crash mongot at startup** (verified 2026-05-03). If the operator renders a hosts list with an unreachable / not-in-RS host, the mongot pod times out during topology discovery and exits. Phase 2 should derive the hosts list deterministically from MongoDBMulti pod-svc FQDNs (which the test fixture already does) — no human typing of hostnames into the CR. For prod, customers populate the hosts list from their RS member list; documenting how to do this safely is a Phase 8 docs item.
- **mongot upstream `readPreferenceTags`** — confirmed not yet implemented (agent verified at `MongoConnectionConfig.java:73-83`). Permanent path requires upstream mongot work. No further action this spec.
- **CLARIFY-1 (resolved by simplification)** — moot now. Phase 2 renders every mongot config from top-level `external.hostAndPorts`, no per-cluster hosts plumbing. The B14+B16 scaffolding handles the per-cluster reconcile loop; Phase 2 just plugs the same source list into each iteration.
- **Voyage API key in CI** — `AI_MONGODB_EMBEDDING_QUERY_KEY` already wired for single-cluster auto-embedding tests; Phase 2 just reuses it. Verify the new MC RS Evergreen task projection includes it.
- **Docker image pinning** — mongot version floor check is deferred (Phase 8). MVP assumes all member clusters run a mongot that already supports auto-embedding pod-0 leader. If a cluster runs an older mongot, `$vectorSearch` will fail with a non-friendly error; documented as a known limitation, not a defect.

## Cross-references

- [`2026-05-03-mc-mvp-later-phases.md`](./2026-05-03-mc-mvp-later-phases.md) — Phase 3 (Q2-Sh-MC), Phase 4 (Q1-RS-MC), Phase 5 (Q1-Sh-MC) holding pen. Will be re-brainstormed once Phase 2 is green.
