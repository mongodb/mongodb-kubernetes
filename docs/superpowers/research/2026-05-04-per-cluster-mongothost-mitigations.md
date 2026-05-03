# Per-cluster `mongotHost` on `MongoDBMultiCluster`: gap analysis and mitigation options

**Date:** 2026-05-04
**Author:** Investigation for MC search Phase 2 MVP
**Status:** Research only — no code changes
**Context:** Phase 2 plan at `docs/superpowers/plans/2026-05-03-mc-mvp-base-and-phase2.md`, spec at `docs/superpowers/specs/2026-04-30-rs-mc-mvp-to-green-design.md`

---

## TL;DR

The Phase 2 plan and the MVP spec both pre-committed to setting `mongotHost` per cluster
via `MongoDBMulti.spec.clusterSpecList[i].additionalMongodConfig.setParameter.mongotHost`.
That field does not exist. `ClusterSpecItem` exposes only `clusterName`,
`service`, `externalAccess`, `members`, `memberConfig`, `statefulSet`, `podSpec` —
no `additionalMongodConfig` (`api/v1/mdb/mongodb_types.go:270-289`,
`helm_chart/crds/mongodb.com_mongodbmulticluster.yaml:390-444`). Top-level
`additionalMongodConfig` (on `DbCommonSpec`) is the only knob, and the
operator's process-build path applies that single top-level value to every
process across every cluster
(`controllers/om/process/om_process.go:58`, every process gets
`mrs.Spec.GetAdditionalMongodConfig()`).

Net result: the plan's Task 24 cannot land as written. Phase 2 either ships
without per-cluster locality (top-level `mongotHost` to one cluster's proxy-svc,
cross-cluster traffic via Istio — what the iter-3/iter-4 e2e currently does)
or the operator gains a small CRD + reconcile change (Option A below) before
Phase 2 closes.

**Recommendation:** Land Option A (extend `ClusterSpecItem` with
`AdditionalMongodConfig`). It implements the primitive the plan and spec
already assumed; matches the operator's existing per-cluster-field pattern
(`MemberConfig`, `StatefulSetConfiguration`, `ExternalAccessConfiguration`);
unblocks Task 24 verbatim; ~1–2 days of work with tests. If that slips past
the 2026-05-04 morning Phase 2 MVP target, ship Phase 2 at scaffold parity
(top-level `mongotHost`, no per-cluster locality) and land Option A as a
Phase 2.5 micro-PR — see "Time-pressure tension" below.

---

## 1. Gap proof

### 1.1 `ClusterSpecItem` has no `AdditionalMongodConfig`

`api/v1/mdb/mongodb_types.go:270-289`:

```go
type ClusterSpecItem struct {
    ClusterName                 string
    Service                     string
    ExternalAccessConfiguration *ExternalAccessConfiguration
    Members                     int
    MemberConfig                []automationconfig.MemberOptions
    StatefulSetConfiguration    *common.StatefulSetConfiguration
    PodSpec                     *MongoDbPodSpec
}
```

The CRD schema confirms it
(`helm_chart/crds/mongodb.com_mongodbmulticluster.yaml:390-444`):

```yaml
clusterSpecList:
  items:
    properties:
      clusterName: {...}
      externalAccess: {...}
      memberConfig: {...}
      members: {...}
      podSpec: {...}
      service: {...}
      statefulSet: {...}
```

No `additionalMongodConfig`. Same gap exists in `ClusterSpecItemOverride`
(used by sharded `shardOverrides[].clusterSpecList`,
`api/v1/mdb/mongodb_types.go:295-311`) — that struct also lacks
`AdditionalMongodConfig`. So per-cluster `additionalMongodConfig` is missing
*everywhere* in the operator's per-cluster API surface.

`additionalMongodConfig` is exposed only at the top level via
`DbCommonSpec.AdditionalMongodConfig` (`api/v1/mdb/mongodb_types.go:418`)
and at the *component* level for sharded
(`ShardedClusterComponentSpec.AdditionalMongodConfig`,
`api/v1/mdb/shardedcluster.go:37`) — neither dimension is per-cluster.

### 1.2 Process build is single-source for all clusters

`controllers/om/process/om_process.go:38-62`,
`CreateMongodProcessesWithLimitMulti`:

```go
processes := make([]om.Process, len(hostnames))
for idx := range hostnames {
    processes[idx] = om.NewMongodProcess(
        ..., mongoDBImage, forceEnterprise,
        mrs.Spec.GetAdditionalMongodConfig(),  // SAME top-level value for every process
        &mrs.Spec, certFileName, mrs.Annotations, ...)
}
```

There is no per-cluster lookup; the reducer over `clusterSpecList` only
emits hostnames and indices, then re-reads the top-level config for every
process. To support per-cluster differentiation, the per-cluster value must
enter at this call site (or be baked into a per-cluster config object that
the loop reads).

### 1.3 Plan + spec already pre-committed to a field that doesn't exist

- Plan Task 24 (`docs/superpowers/plans/2026-05-03-mc-mvp-base-and-phase2.md:2156-2204`) instructs the test fixture to set
  `clusterSpecList[i].additionalMongodConfig.setParameter.mongotHost`.
- Plan architecture summary (line 7) and table (line 42) reference the same field.
- Spec line 159 (`docs/superpowers/specs/2026-04-30-rs-mc-mvp-to-green-design.md:159`):

  > In MongoDBMulti, this is set via `spec.clusterSpecList[i].additionalMongodConfig.setParameter.mongotHost` — the per-cluster override field.

- Spec line 309 reaffirms it.

The field is referenced ~6 times across plan + spec as if it already existed.
This investigation confirms it does not. The spec/plan blocker is real, not a
test-author misreading.

### 1.4 What the e2e currently does (iter-3/iter-4 scaffold)

The current `mc-search-phase2-q2-rs` branch
(`docker/mongodb-kubernetes-tests/tests/multicluster_search/q2_mc_rs_steady.py:152-179`)
sets *no* `mongotHost` at all and explicitly defers data-plane assertions:

```python
# NOTE: MongoDBMulti pods refuse to reach Ready when
# additionalMongodConfig.setParameter.mongotHost + searchTLSMode=requireTLS
# is set at create-time ...
# Without mongotHost on the source, mongod-side $search forwarding can't
# work — the data-plane portion of this test is therefore deferred to
# Phase 3 implementation ...
```

So today the test is scaffold-green only. To flip to real-coverage green
(plan §Phase 2 G2 acceptance gate) we need either:

- a per-cluster mongotHost (Option A or D/E/F), or
- a top-level mongotHost pointed at one cluster's proxy-svc and accept
  cross-cluster Istio-routed query traffic (Option E', the "scaffold parity"
  fallback).

---

## 2. Mitigation options

| ID | Approach | Effort | Blast radius | Recommend? |
|---|---|---|---|---|
| **A** | **Extend `ClusterSpecItem` with `AdditionalMongodConfig`** | Small/Medium (~1–2d) | CRD additive change + one operator function + cleanup-map handling | **Yes — primary** |
| B | Reuse top-level `additionalMongodConfig` + AC override per-cluster | Medium/Large | Plumbs new per-cluster path through automation config layer | No |
| C | Post-deploy mongosh `db.adminCommand({setParameter})` | Small | Test-only hack; agent overwrites at next reconcile | No (test demo only) |
| D | Multi-host `mongotHost` (mongot picks closest) | Large + external | Requires mongot server change | No (post-MVP) |
| E | Per-cluster DNS resolves shared FQDN to local Envoy | Medium | Brittle; cluster-DNS infra dependency | No |
| E' | Top-level `mongotHost` to one cluster (status-quo scaffold) | Trivial | No locality; cross-cluster Istio traffic | Fallback only |
| F | `readPreferenceTags` for sync source locality | External + Large | Mongot doesn't honor tags yet | No (post-MVP, already in spec) |

### Option A — Extend `ClusterSpecItem` with `AdditionalMongodConfig`

**Approach:**

1. Add `AdditionalMongodConfig *AdditionalMongodConfig` to `ClusterSpecItem`
   in `api/v1/mdb/mongodb_types.go:270-289`. This is additive — empty
   `clusterSpecList` items continue to inherit the top-level value (existing
   behaviour preserved).
2. Regenerate `api/v1/mdb/zz_generated.deepcopy.go` and the CRDs at
   `helm_chart/crds/mongodb.com_mongodbmulticluster.yaml` and
   `config/crd/bases/mongodb.com_mongodbmulticluster.yaml`.
3. Modify `CreateMongodProcessesWithLimitMulti`
   (`controllers/om/process/om_process.go:38-62`) to walk `clusterSpecList`
   and, for each cluster, deep-merge top-level + per-cluster
   `additionalMongodConfig` and pass the merged value as `additionalConfig`
   to `om.NewMongodProcess` for that cluster's processes only.
   Merge precedence: per-cluster overrides top-level (matching the
   `ClusterSpecItemOverride` precedence pattern that sharded shardOverrides
   already use, `api/v1/mdb/sharded_cluster_validation.go:312-327`).
4. Handle the `lastMongodbConfig` cleanup map at
   `controllers/operator/mongodbmultireplicaset_controller.go:764` (and
   `common_controller.go:1083`,
   `ReconcileReplicaSetAC` → `MergeReplicaSet(rs, specArgs26, prevArgs26, log)`).
   The cleanup logic at
   `pkg/util/maputil/maputil.go:114` (`RemoveFieldsBasedOnDesiredAndPrevious`)
   uses a single `desiredMap` to know what fields are still desired.
   With per-cluster diversity, the simplest correct semantics is to feed the
   *union* of per-cluster + top-level keys as `desiredMap` — over-keeps, never
   under-removes. Known sharp edge: cluster A drops key X while cluster B
   keeps X means cluster A's process retains stale X for one reconcile until
   cluster B also drops it. Acceptable for MVP — `mongotHost` doesn't
   "go away" mid-deployment.
5. Tests:
   - `controllers/om/process/om_process_test.go` — add a per-cluster merge test.
   - `controllers/operator/mongodbmultireplicaset_controller_test.go` — assert
     per-cluster process gets per-cluster `setParameter.mongotHost`.
6. Plan Task 24 lands **as written** — fixture sets
   `clusterSpecList[i].additionalMongodConfig.setParameter.mongotHost`
   verbatim.

**Files touched (estimate, line counts approximate):**

- `api/v1/mdb/mongodb_types.go` — add ~5 lines (one optional field with json tag and comment)
- `api/v1/mdb/zz_generated.deepcopy.go` — regenerated
- `helm_chart/crds/mongodb.com_mongodbmulticluster.yaml` — regenerated
- `config/crd/bases/mongodb.com_mongodbmulticluster.yaml` — regenerated
- `controllers/om/process/om_process.go` — ~10 lines
- `controllers/operator/mongodbmultireplicaset_controller.go` — cleanup-map union (~5 lines)
- `controllers/om/process/om_process_test.go` — ~30 lines test
- `controllers/operator/mongodbmultireplicaset_controller_test.go` — ~50 lines test

**Pros:**

- Implements the primitive the plan + spec already assumed. No design rework.
- Matches the operator's existing per-cluster-field pattern: `MemberConfig`,
  `StatefulSetConfiguration`, `ExternalAccessConfiguration`,
  `PodSpec`, `Service` are all already optional per-cluster overrides.
  `AdditionalMongodConfig` simply joins that set.
- Additive CRD change — no breaking schema, no migration burden, no version bump.
- Per-cluster precedence is well-trodden: sharded `ShardOverride` does the
  same merge with `ClusterSpecItemOverride`.

**Cons / sharp edges:**

- `lastMongodbConfig` is a flat union map (described above). Edge case but
  worth flagging in PR.
- Operator code change, not test-only — requires unit test + integration test
  coverage and a real review cycle.
- CRD regeneration touches both `helm_chart/crds/...` and
  `config/crd/bases/...` and the embedded test copy at
  `docker/mongodb-kubernetes-tests/helm_chart/crds/...` — easy to forget one.

**Effort:** Small/Medium. ~1–2 days realistic with proper tests
(operator code is small; CRD regen + multi-test surface adds non-trivial
overhead).

**Blast radius:** Low. Additive field; absent value preserves current
behaviour exactly.

### Option B — AC-layer override only (no CRD field)

**Approach:** Keep CRD as-is. In `CreateMongodProcessesWithLimitMulti`,
introduce a per-cluster mutator (e.g., a callback or annotations-driven
override) that injects `setParameter.mongotHost` into specific processes
based on cluster index. The user expresses intent through annotations or a
side-channel.

**Pros:** No CRD change.

**Cons:**

- No declarative API surface — users can't set per-cluster mongod config
  through normal spec mechanics. Annotations or env vars are not the right
  shape for ongoing customer-facing per-cluster knobs.
- Search-controller-specific glue would have to know about MongoDBMulti
  internals, blurring the controller boundary.
- Loses the existing per-cluster pattern symmetry that A retains.
- Test fixture would need a private mechanism, not normal spec → harder to
  document and harder to keep in sync with what an end-customer would do.

**Verdict:** No. If we're going to plumb per-cluster differentiation through
the process-build path anyway (Option B does this), exposing it via the CRD
field (Option A) costs almost nothing extra and is the right shape.

### Option C — Post-deploy mongosh `setParameter`

**Approach:** After mongod pods reach Ready, the e2e (or an out-of-band
script) runs `db.adminCommand({setParameter: 1, mongotHost: '<local-fqdn>'})`
on each cluster's primary, choosing the cluster-local FQDN.

**Pros:** No CRD change, no operator change. Could be wedged into a test
helper today.

**Cons:**

- **Test-only.** The Ops Manager / Cloud Manager automation agent owns
  `setParameter` and will overwrite anything set out-of-band on its next
  reconcile. So this works exactly until the agent next applies
  `additionalMongodConfig` from the AC, which is generally on every
  reconciliation cycle.
- `setParameter` for `mongotHost` requires it to be a known runtime-settable
  parameter — verify against MongoDB version's parameter list before relying
  on this. (Out of scope for this report; flag for the test-author if Option C
  is used as a demo only.)
- Loses on mongod restart — every restart re-applies the AC's
  `additionalMongodConfig` (if set) or starts without `mongotHost` (if not).

**Verdict:** Useful as a *test-only* demonstration vehicle to prove the
data-plane works once locality is in place — e.g., a one-off script run after
the source RS is Ready that proves per-cluster `$search` would return rows.
Do not recommend as the production path.

### Option D — Multi-host `mongotHost` (mongot picks closest)

Mongot-server change. Out of scope for the operator; post-MVP per existing
spec. Not recommended for Phase 2.

### Option E — Per-cluster DNS scoping

CoreDNS rewrites or per-cluster service-mesh tricks to make a single shared
FQDN resolve to each cluster's local Envoy. Brittle; introduces cluster-DNS
infrastructure dependency; doesn't compose with multi-tenant clusters. Not
recommended.

### Option E' — Status-quo scaffold (top-level `mongotHost`)

Top-level `additionalMongodConfig.setParameter.mongotHost` set to cluster-0's
proxy-svc FQDN. All clusters' mongods route to cluster-0's local Envoy →
cluster-0's mongot pool. Cross-cluster mongod→mongot traffic flows through
Istio.

**Pros:**

- Trivial. Zero CRD change, zero operator change. Just wire it in the test
  fixture.
- Data-plane works (Phase 2 G2 acceptance can be met if you accept the
  cross-cluster routing).

**Cons:**

- **No per-cluster locality.** Defeats the value-add of Phase 2 MC search.
- The existing test scaffold (iter-3/iter-4) hit "Phase=Pending for 25min on
  `mdb-rs-q2-mc-0`" with this shape — the `q2_mc_rs_steady.py` author
  documented at line 167-178 that mongod startup-side validation of
  `searchIndexManagementHostAndPort` plus cross-cluster Service DNS
  resolution lag in the MC harness blocks pod readiness. Worth re-running
  with the latest harness to verify the failure mode is reproducible — it
  may have been a B5/Phase 2 ordering issue that was resolved.

**Verdict:** Acceptable as a **Phase 2 fallback** if Option A slips. Phase
2.5 then closes the locality gap.

### Option F — `readPreferenceTags`

Mongot must add `readPreferenceTags` support. Already documented in the spec
as post-MVP (lines 30-37, 70). Not recommended for Phase 2.

---

## 3. Recommendation

**Land Option A.** It implements the primitive the plan and spec already
assumed and matches the operator's existing per-cluster-field pattern (every
other per-cluster-overridable field on `ClusterSpecItem` is already there;
`AdditionalMongodConfig` simply joins the set). Effort is small/medium —
~1–2 days realistic with tests — and unblocks Plan Task 24 verbatim.

**Why it survives a design review:**

1. The CRD change is additive and orthogonal to existing behaviour. Existing
   `MongoDBMulti` resources without `clusterSpecList[i].additionalMongodConfig`
   continue to behave exactly as before.
2. The merge precedence (per-cluster overrides top-level) matches the
   sharded-cluster `ShardOverride` pattern already in the operator —
   reviewers don't have to invent new semantics.
3. The change is in `CreateMongodProcessesWithLimitMulti` only — a single
   well-tested function. No broad refactor.
4. Test surface is small and self-contained.

**The MongoDBSearch-mongotHost wiring is incidental to Option A.**
Option A is the right shape regardless of whether Phase 2 uses it for
mongotHost. Future per-cluster knobs (e.g., `wiredTiger.engineConfig.cacheSizeGB`
to differ between cold-storage vs hot-cache regions) get the same primitive
for free.

---

## 4. Time-pressure tension (Phase 2 morning of 2026-05-04)

Option A is operator code, not test code. If it lands cleanly and on time,
Plan Task 24 ships verbatim and Phase 2 closes with real per-cluster
locality. If it slips:

### Path 1 — Phase 2 MVP at scaffold parity (Option E')

- Keep top-level `additionalMongodConfig.setParameter.mongotHost` pointed at
  cluster-0's `{name}-search-0-proxy-svc` FQDN.
- Accept cross-cluster Istio-routed query traffic (cluster-1 mongods →
  cluster-0 Envoy → cluster-0 mongot pool).
- Re-verify the iter-3 "Phase=Pending for 25min" failure mode against the
  current harness — it may have been a B5 ordering issue now resolved.
- Spec the locality gap as a Phase 2.5 deliverable that lands Option A.
- Plan Task 24 changes to "set top-level mongotHost"; the per-cluster
  `clusterSpecList[i].additionalMongodConfig` shape is deferred.

  **Honest cost:** Phase 2 ships data-plane correctness without locality.
  The "value-add" framing in the spec (per-cluster Envoy + per-cluster
  mongot pool) holds for the *index-fill* path (mongot reads from any RS
  member it picks via topology discovery, locality-pinned by Envoy on the
  query-return path? — verify against B16 Envoy config), but the *query*
  path mongod→mongot is NOT cluster-local in this fallback.

### Path 2 — Slip Phase 2 by 1–2 days, land Option A first

Cleanest. The plan Task 24 needs to materialize anyway. Doing the operator
work first means Phase 2 lands with the spec's full value-add intact.

**My recommendation:** If Option A is achievable by EOD 2026-05-04, slip
Phase 2 by one day and ship the right primitive. If Option A slips, ship
Phase 2 at scaffold parity (Path 1) and land Option A as Phase 2.5 within
the same week. Do not ship Option C (mongosh) as the production answer —
it works exactly until the next reconcile.

---

## 5. Implementation sketch — Option A

For an implementer, the work outline:

```
1. api/v1/mdb/mongodb_types.go — ClusterSpecItem
   + AdditionalMongodConfig *AdditionalMongodConfig `json:"additionalMongodConfig,omitempty"`
   (with kubebuilder pruning preserve-unknown-fields, like DbCommonSpec.AdditionalMongodConfig)

2. make generate / make manifests (regenerates deepcopy + all 3 CRD copies)

3. controllers/om/process/om_process.go::CreateMongodProcessesWithLimitMulti
   - Build a merged config per ClusterSpec at the top of the loop
   - Pass it to NewMongodProcess for that cluster's processes only
   - Merge order: deep-merge of top-level into per-cluster (per-cluster wins)
   - Keep top-level fallback when ClusterSpec.AdditionalMongodConfig is nil

4. controllers/operator/mongodbmultireplicaset_controller.go
   - For lastMongodbConfig: union of top-level + all per-cluster keys, fed as
     desiredMap to RemoveFieldsBasedOnDesiredAndPrevious. Annotation should
     persist the same union for prevArgs26 compatibility.
   - GetLastAdditionalMongodConfig() at api/v1/mdbmulti/mongodb_multi_types.go:437
     should return the union.

5. Tests:
   - Unit: CreateMongodProcessesWithLimitMulti with two clusters, distinct
     setParameter values, assert each process has its cluster's value.
   - Integration: ReconcileReplicaSetAC + MergeReplicaSet writes distinct
     args2_6.setParameter.mongotHost per process.
   - E2E (q2_mc_rs_steady.py): Plan Task 24 lands as written.

6. Validation:
   - MongoDBMulti without per-cluster additionalMongodConfig still reconciles
     identically (no regression for existing customers).
   - MongoDBMulti with one cluster setting per-cluster value, another
     omitting, results in mixed top-level/per-cluster behaviour.
```

### Sharp edge to flag in PR

`RemoveFieldsBasedOnDesiredAndPrevious` over the union of all per-cluster keys
means: if cluster A drops key X (e.g., user stops setting one parameter on
cluster A only) while cluster B still keeps X, cluster A's process retains
stale X for one reconcile cycle until cluster B also drops X (or the union
shrinks). For `mongotHost` specifically this never matters (operator owns
that key when search wiring lands). Document it; don't over-engineer.

---

## 6. Considered and rejected (one-liners)

- **D** — multi-host `mongotHost`: requires mongot server change; post-MVP.
- **E** — per-cluster DNS scoping for shared FQDN: brittle, infra-coupled.
- **F** — `readPreferenceTags` for sync source locality: requires mongot
  support; spec already defers as post-MVP (lines 30-37).

---

## 7. Open questions for follow-up

1. Does the iter-3 "Phase=Pending for 25min" failure (when top-level
   `mongotHost + searchTLSMode=requireTLS` is set on the source) reproduce on
   the current harness post-B5? If not, Path 1 (scaffold parity) is cheaper.
   If yes, Option A is required even for the fallback to work.
2. Is `mongotHost` a runtime-settable `setParameter` (relevant only to Option
   C as a demo)? Check against the target MongoDB version's parameter
   manifest.
3. Should `ClusterSpecItemOverride` (used by sharded `shardOverrides`) get
   the same field for consistency? Not required for Phase 2; flag for a
   future cleanup.

---

## 8. References

- Plan: `docs/superpowers/plans/2026-05-03-mc-mvp-base-and-phase2.md`
  (Task 24 lines 2156-2217; spec table line 42; architecture line 7)
- Spec: `docs/superpowers/specs/2026-04-30-rs-mc-mvp-to-green-design.md`
  (lines 30-37, 67-70, 159-166, 309)
- Type: `api/v1/mdb/mongodb_types.go:266-311` (ClusterSpecItem,
  ClusterSpecItemOverride), `api/v1/mdb/mongodb_types.go:377-434` (DbCommonSpec)
- CRD: `helm_chart/crds/mongodb.com_mongodbmulticluster.yaml:390-444`
- Process build: `controllers/om/process/om_process.go:38-62`
- AC merge: `controllers/operator/common_controller.go:1072-1097`,
  `controllers/om/deployment.go:146-184`,
  `controllers/om/process.go:476-508`
- Cleanup: `pkg/util/maputil/maputil.go:111-140`
- Sharded precedent: `api/v1/mdb/shardedcluster.go:35-49`,
  `api/v1/mdb/sharded_cluster_validation.go:312-327`
- E2E status: `docker/mongodb-kubernetes-tests/tests/multicluster_search/q2_mc_rs_steady.py:152-179`
