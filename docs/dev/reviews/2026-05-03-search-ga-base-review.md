# Review: search/ga-base — full B-train + simplify + #1050

**Date:** 2026-05-03
**Reviewer:** Senior code-review pass before Phase 2 lands
**Scope:**

- `git diff origin/master..origin/search/ga-base` (8 PRs squash-merged: B1, B14+B18, B16, B3+B4+B13, B5, B8, B9, harness #1047, simplify #1049)
- `git diff origin/master..origin/search/strip-per-cluster-envoy-replicas` (PR #1050, MERGEABLE-but-unmerged)
- Tag `base-pr-train-green` at `36b318d09` (the post-B9 pre-simplify checkpoint)

**Verification performed during review:**

- `go test ./api/v1/search/... ./controllers/searchcontroller/... ./controllers/operator/... ./pkg/handler/...` on ga-base — all green
- `make manifests` against ga-base — 0 diff (no CRD drift)
- One ad-hoc test (deleted) confirmed Blocker B1 below empirically — the unit test fails as predicted on ga-base today

---

## TL;DR — what the train delivers and what's blocking Phase 2

ga-base is mostly sound architectural scaffolding. CRD types are well-shaped, validation is comprehensive in coverage, status surface is clean, and the per-member-cluster watch wiring is in place. The cleanup pass is honest — comments on unchanged code paths, no behavior changes.

**One blocker** must land before Phase 2 starts the per-cluster fan-out: the per-cluster Envoy Pod template references the wrong (non-suffixed) ConfigMap volume in MC mode. The pod will fail to start in any member cluster. This is empirically verified — see B1 below.

Three concerns are worth fixing on ga-base or explicitly deferring with a tracker:

- C1: cross-cluster watch routing is split across two mechanisms (label vs annotation). The annotation arm has zero writers anywhere in the train — dead infrastructure.
- C2: `validators` slice has no registration test. A new validator that's defined but not referenced will silently never run.
- C3: per-cluster status fields exist on the spec but are never populated by the search controller; only the Envoy controller writes the `loadBalancer.clusters[]` slice.

The remaining items are nits or forward-risk that Phase 2 will trip over and is therefore well-positioned to fix.

---

## 1. Correctness

### 🔴 B1 — Per-cluster Envoy Pod mounts the wrong (non-suffixed) ConfigMap

**File:** `controllers/operator/mongodbsearchenvoy_controller.go`
**Lines:** `522` (call site) + `548-558` (`buildEnvoyPodSpec` + volume name)
**Severity:** Blocker

`ensureConfigMap` at line 458-481 writes the per-cluster ConfigMap with the per-cluster name `search.LoadBalancerConfigMapNameForCluster(clusterName)` — e.g. `mdb-search-search-lb-0-a-config` for member cluster `a`. This is correct.

`ensureDeployment` at line 522 calls `buildEnvoyPodSpec(search, tlsCfg, tlsEnabled, image, resources, managedSecurityContext)` — without `clusterName`. `buildEnvoyPodSpec` then hardcodes the volume to `search.LoadBalancerConfigMapName()` (no per-cluster suffix) at line 554:

```go
LocalObjectReference: corev1.LocalObjectReference{Name: search.LoadBalancerConfigMapName()},
```

In MC mode, the Pod template references `mdb-search-search-lb-0-config`, but only `mdb-search-search-lb-0-a-config` exists in member cluster `a`. The pod's volume mount fails; Envoy never starts; no SNI routing happens; Phase 2's data plane is dead on arrival.

**Why no test caught this:** the existing `TestEnsureDeployment_*` cases verify the persisted `Deployment` object (replica count, owner ref) but never inspect `dep.Spec.Template.Spec.Volumes[*].ConfigMap.Name`. I authored a focused unit test during review that hits exactly this path; it fails on ga-base today:

```
Error: Not equal:
  expected: "mdb-search-search-lb-0-a-config"
  actual  : "mdb-search-search-lb-0-config"
```

(Test deleted; the proof is reproducible by inspecting any persisted Deployment after `ensureDeployment` runs against MC mode.)

**Fix:**

1. Thread `clusterName` into `buildEnvoyPodSpec`.
2. Replace `search.LoadBalancerConfigMapName()` with `search.LoadBalancerConfigMapNameForCluster(clusterName)` at the volume site.
3. Add a regression test that inspects `dep.Spec.Template.Spec.Volumes` for the envoy-config volume and asserts `ConfigMap.Name == search.LoadBalancerConfigMapNameForCluster(clusterName)` in both single-cluster (`""`) and MC modes.

This must land on ga-base before Phase 2's fan-out, OR Phase 2 must include the fix. The bug is Envoy-controller-internal — not blocked by Phase 2's per-cluster reconcile work in the search helper.

### 🟠 C1 — Cross-cluster watch routing has two divergent mechanisms; the annotation arm has zero writers

**Files:**

- `controllers/operator/mongodbsearchenvoy_controller.go:67-68, 675-686, 736-747` — Envoy controller uses **labels** (`mongodb.com/search-name`, `mongodb.com/search-namespace`)
- `controllers/operator/mongodbsearch_controller.go:297-312` + `controllers/operator/watch/predicates.go:154-173` + `pkg/handler/enqueue_owner_multi_search.go:14-65` — Search controller uses **annotation** `mongodb.com/v1.MongoDBSearchResource`

**Severity:** Concern

The Envoy controller stamps two labels on every Deployment/ConfigMap it writes (line 675-686) and routes member-cluster watches via `mapEnvoyObjectToSearch` (line 736-747) which reads those labels. This works.

The search controller registers per-member-cluster watches on Service / Secret / ConfigMap / StatefulSet / Deployment via `EnqueueRequestForSearchOwnerMultiCluster` (uses `MongoDBSearchResourceAnnotation`) and gates them with `PredicatesForMultiClusterSearchResource`. **Nothing in ga-base writes that annotation onto any object in any cluster** — `grep -rn "MongoDBSearchResourceAnnotation" /tmp/gabase/` finds the constant, the predicate that reads it, and the handler that reads it, but no SetAnnotations / no patch / no metadata.Annotations write.

So today on ga-base, the search controller's member-cluster watches are wired but inert. No event routes home.

**Why this matters:**

1. When Phase 2 starts writing per-cluster mongot StatefulSets / Services / ConfigMaps to member clusters, it must either (a) stamp the annotation at the write site so existing watches actually fire, or (b) switch the watches to a label-based mapper consistent with the Envoy controller's pattern. If neither happens, status drift between actual cluster state and the central CR will go un-noticed.
2. Two mechanisms is a maintenance hazard: a future contributor will pick whichever they see first and the inconsistency compounds.

**Recommendation:** unify on the label scheme (Envoy controller's pattern is closer to MongoDBMulti precedent). On ga-base, add a `// TODO(phase-2):` comment at the search-controller's watch registration noting that annotations are not yet stamped, OR remove the dead watches and let Phase 2 add them back with the writer side. Either is fine; leaving both halves silently broken is the wrong move.

### 🟠 C2 — `validators` slice has no registration test

**File:** `api/v1/search/mongodbsearch_validation.go:40-72`

The `RunValidations` function holds a slice of 19 validators. Each is unit-tested in isolation (`TestValidateClustersUniqueClusterName`, etc.). **No test invokes `RunValidations()` itself or asserts that every defined `validateClusters*` / `validateMC*` symbol is registered.**

A new validator that's defined but accidentally not added to the slice would compile, all unit tests would pass, and the rule would silently never run. PR #1050 is the canonical example: it adds `validateClustersNoPerClusterEnvoyReplicas` as a free function AND adds it to the slice. The slice change is the load-bearing change; the function existing is not enough.

**Recommendation:** add one test using reflection or a hand-maintained map:

```go
func TestRunValidations_AllValidatorsRegistered(t *testing.T) {
    s := &MongoDBSearch{...}
    // sanity: each validator that exists in this package must be referenced
    // in the validators slice. Cheapest form: hand-maintained list, dies on
    // missing entry the next time someone adds a validator without wiring it.
    expected := []string{
        "validateLBConfig", "validateUnmanagedLBConfig", ...
    }
    // assert that every name in `expected` appears as a string in the
    // function-name map of the validators slice (use runtime.FuncForPC).
}
```

Cost: ~30 lines. Catches the entire class.

### 🟠 C3 — `ShardLoadBalancerStatus` and per-cluster status are defined but never populated by the search controller

**File:** `api/v1/search/mongodbsearch_types.go:407-416, 467-472` — types defined; printed in CRD
**Files:** `controllers/searchcontroller/mongodbsearch_reconcile_helper.go:229-245` — `buildPerClusterStatusItems` populates only the `clusterName + status.Common` (Phase, Message); `LoadBalancer`, `Warnings`, `ObservedReplicas` fields stay nil/zero.

**Severity:** Concern

The CRD exposes:

- `status.clusterStatusList.clusterStatuses[i].observedReplicas` — never written
- `status.clusterStatusList.clusterStatuses[i].warnings` — never written
- `status.clusterStatusList.clusterStatuses[i].loadBalancer` — never written by search controller (Envoy controller has its own writer at line 175-179, but it lives on `status.loadBalancer.clusters[i]` not `status.clusterStatusList.clusterStatuses[i].loadBalancer`)
- `status.shardLoadBalancerStatusInClusters` — never written anywhere

`SearchClusterStatusItem.LoadBalancer *LoadBalancerStatus` is doc-commented as the per-cluster RS-mode LB status, but the Envoy controller writes a different shape (`LoadBalancerStatus.Clusters []ClusterLoadBalancerStatus`). Two flat-vs-nested representations of "per-cluster LB phase" co-exist in the CRD.

Phase 2 will need to pick one. ga-base shipping both is a 🟠 because it lets either one win in a future PR without an obvious choice surfaced.

**Recommendation:** decide now whether `SearchClusterStatusItem.LoadBalancer` (per-cluster nested) or `LoadBalancerStatus.Clusters[]` (flat sibling) is the canonical place. If the latter, drop the field on the former with a `Deprecated:` marker so a CRD bump removes it. If the former, the Envoy controller needs to write into `clusterStatusList.clusterStatuses[i].loadBalancer` instead. Document the choice in `2026-04-30-rs-mc-mvp-to-green-design.md`.

### ✅ Sound — cluster-index annotation logic

`ensureClusterIndexAnnotation` (`controllers/searchcontroller/mongodbsearch_reconcile_helper.go:416-451`) plus `AssignClusterIndices` (`api/v1/search/cluster_index.go:17-34`) are tight:

- monotonic; never reuses on remove/re-add (`TestAssignClusterIndices` covers all 8 scenarios)
- malformed annotation gracefully recovers (line 429-435 logs + rebuilds)
- equal-string comparison short-circuits the no-op patch (line 444-446) — relies on Go's stable map-key sort in `json.Marshal` which is guaranteed since 1.12
- in-memory `r.mdbSearch.Annotations` is updated by `annotations.SetAnnotations` (line 74 of `pkg/kube/annotations/annotations.go`) so subsequent placeholder resolution sees the new mapping in the same reconcile

The atomicity question raised in the brief — "what if reconcile crashes between `clusters` change and annotation update?" — is benign because `AssignClusterIndices` is idempotent: re-running the function with the same `existing` and `current` returns the same mapping. So a crash before the patch lands just retries on the next reconcile and arrives at the same answer.

### ✅ Sound — per-cluster Envoy writes do not cross-stamp owner refs

`controllers/operator/mongodbsearchenvoy_controller.go:467-474, 533-536` — owner ref is set only when `clusterName == ""` (central). Member-cluster writes return nil from the mutate fn, leaving owner refs empty. Comments at lines 451-457 make the rationale explicit. The deliberate inconsistency vs the search controller (where `applyReconcileUnit` will stamp owner refs in member clusters per Phase 2) is reasonable: the Envoy controller has explicit cleanup at `deleteEnvoyResources` (line 302-318) to compensate. Phase 2 should follow the same pattern (no cross-cluster owner refs) to stay consistent.

### ✅ Sound — `WorstOfPhase` aggregation

`api/v1/search/status_aggregation.go:8-47` — handles empty input (returns empty Phase), mixed phases (Failed beats Pending beats Running), unknown phases (rank -1, any known phase wins), and a clever sentinel (`worstRank = -2` so the first input always replaces the empty default). Tests at `status_aggregation_test.go` cover the four interesting cases. No action.

### ✅ Sound — `EffectiveClusters` auto-promotion

`api/v1/search/mongodbsearch_types.go:827-840` — single legitimate read of the deprecated top-level fields, isolated to this helper. The `nolint:staticcheck SA1019` is correctly scoped (just this one function). Returns the slice as-is when set (including empty slice), promotes the deprecated fields when nil. Tests at `mongodbsearch_types_test.go:31-94` cover empty, single-entry, and pure-MC cases. The pointer-of-slice trick (`*[]ClusterSpec`) to distinguish "omitted" from "explicitly empty" is correct.

---

## 2. Architecture / design

### 🟡 Spec field deprecation strategy

`api/v1/search/mongodbsearch_types.go:78-114` — `Replicas`, `StatefulSetConfiguration`, `Persistence`, `ResourceRequirements` are marked `// Deprecated:` with a clear migration message. `+kubebuilder:default=1` was removed from `Replicas` (PR #1030 commit message explains the rationale: server-side defaulting hid intent). The mutual-exclusion validator at `validateClustersAndTopLevelFieldsMutuallyExclusive` (line 444-465) rejects setting both.

This is well done. The deprecation message is user-friendly. One small nit: the godoc says "Deprecated: In multi-cluster deployments..." but the rule applies in **all** deployments where `spec.clusters` is set, not just MC. Tweak phrasing to "Deprecated: When spec.clusters is set, use spec.clusters[].replicas instead." Same applies to the other three fields.

### 🟡 Per-cluster Envoy naming uses `clusterName`, not `clusterIndex`

`api/v1/search/mongodbsearch_types.go:986-1000` — `LoadBalancerDeploymentNameForCluster` and `LoadBalancerConfigMapNameForCluster` build names with `clusterName`, not `clusterIndex`. Spec at line 145 says this is intentional ("disambiguates clusters from MCK's central client"). This is fine, but worth noting the implication:

- DNS-1123 labels max 63 chars. With `name="my-search"` and a long clusterName, the per-cluster Deployment name `my-search-search-lb-0-{clusterName}` can hit the limit. `validateClustersEnvoyResourceNames` at `mongodbsearch_validation.go:387-414` correctly checks this at admission. ✅
- If a future need pivots to per-cluster mongot StatefulSet (Phase 2 + Phase 3 territory), those names will use `clusterIndex` per spec line 147. Two naming schemes (clusterName for LB infra, clusterIndex for mongot resources) is a minor cognitive load. Document in Phase 2 spec.

### 🟡 Routing strategy: cross-cluster mongot→mongod sync is acknowledged in spec but not in code comments

The spec (`docs/superpowers/specs/2026-04-30-rs-mc-mvp-to-green-design.md` lines 39-73) clearly explains: every cluster's mongot is seeded with the same top-level `external.hostAndPorts`, so the MongoDB driver discovers the full RS topology and may pick any cluster's mongods as a sync source. mongot→mongod is NOT cluster-local in MVP.

**This understanding is not reflected in the code.** When Phase 2 lands the mongot ConfigMap rendering, the comment at `baseMongotConfig` (currently in `mongodbsearch_reconcile_helper.go:1150`) should call out:

> "Every cluster's mongot ConfigMap renders the same top-level seed list. The MongoDB driver does topology discovery from this seed, so a given mongot may sync from any cluster's mongods. Locality on the *query* side is delivered by the per-cluster Envoy in mongodbsearchenvoy_controller.go; the *sync* direction is intentionally not pinned in MVP. See spec §Routing strategy."

This isn't a ga-base blocker (the rendering is Phase 2's), but it's worth flagging to Phase 2 reviewers so the comment lands at the right line.

### 🟡 Forward-risk: 4-arg helper constructor

`controllers/searchcontroller/mongodbsearch_reconcile_helper.go:85-97` — `NewMongoDBSearchReconcileHelper` takes 4 args; helper has only `client kubernetesClient.Client`. Phase 2 (per the prompt) adds a 5-arg variant that threads `memberClusterClientsMap`. The 4-arg constructor stays for back-compat.

If both constructors live on, a future contributor calling the 4-arg version from a new MC code path will silently get single-client behavior — which is the bug Task 21 caught at `mongodbsearch_controller.go:129` on a Phase 2 worktree. Two ways to mitigate:

1. Mark the 4-arg constructor `// Deprecated:` and have it call the 5-arg with `nil` for the member map; the body of the 5-arg returns single-cluster semantics when the map is empty. One implementation, one entry point for new code.
2. Delete the 4-arg variant entirely on Phase 2 merge and update all callers.

Option 1 is the cheaper migration. Document the decision in Phase 2's plan.

### ✅ Sound — proxy Service stays ClusterIP, no NetworkPolicy templates, no operator-driven Secret replication

Per spec hard-design rules (line 253). Verified by:

- `buildProxyService` (`mongodbsearch_reconcile_helper.go:808-842`) builds `ServiceTypeClusterIP`. No `LoadBalancer` type.
- No `NetworkPolicy` types appear anywhere in the train (`grep -r NetworkPolicy /tmp/gabase/` returns no matches).
- `secret_replicator.py` lives in `docker/mongodb-kubernetes-tests/tests/common/multicluster_search/` (test-only) with explicit comment "MCK operator does NOT replicate Secrets in production". RBAC isn't expanded.

---

## 3. Test coverage

### 🟠 No registration test for `validators` slice (covered above as C2).

### 🟠 No test inspects Pod template volume references (would have caught B1).

`controllers/operator/mongodbsearchenvoy_controller_test.go:575-605, 738-758` — `TestEnsureDeployment_*` tests stop at `dep.Spec.Replicas` and `dep.OwnerReferences`. Neither walks `dep.Spec.Template.Spec.Volumes` for the ConfigMap reference name. Adding such an assertion to `TestEnsureDeployment_PerClusterReplicas_FromClustersSpec` would have caught B1. Worth retrofitting on the B1 fix PR.

### 🟡 Harness primitive: `MCSearchDeploymentHelper.cluster_index` derives from Python dict iteration order

`docker/mongodb-kubernetes-tests/tests/common/multicluster_search/mc_search_deployment_helper.py:38-48` —

```python
self._cluster_indices = {name: idx for idx, name in enumerate(self._member_cluster_clients)}
```

Python dict iteration order is insertion-ordered (since 3.7), so for first-run tests with deterministic `member_cluster_clients` registration, this matches the operator's monotonic `LastClusterNumMapping` exactly.

**Foot-gun:** the operator's annotation-based index is order-of-first-appearance + monotonic-on-add (never reuses). The Python helper is order-of-current-iteration. If a test removes a cluster and re-adds it (post-MVP scenario), the operator preserves the original index; the Python helper assigns a new one. Tests would assert against mismatching indices.

For Phase 2 (which doesn't exercise remove/re-add), this is fine. Document the assumption: "`MCSearchDeploymentHelper` assumes the membership set never changes during a single test run." If a test ever needs to add/remove clusters mid-test, switch to reading the operator's `LastClusterNumMapping` annotation.

### ✅ Sound — harness Python tests have good error-path coverage

`docker/mongodb-kubernetes-tests/tests/common/multicluster_search/test_secret_replicator.py` — three cases: 404 → create, exists/different → patch, exists/same → no-op. Mocked `kubernetes.client.exceptions.ApiException(status=404)` correctly distinguishes 404 from other errors. The non-404 error path correctly re-raises (the implementation at `secret_replicator.py:62-66` matches).

### ✅ Sound — Envoy controller's per-cluster work-list expansion

`controllers/operator/mongodbsearchenvoy_controller_test.go:609-646` — three cases: single-cluster install (no member map), empty `spec.clusters[]` with member map, multi-cluster. Each maps clearly to one of the three branches in `buildClusterWorkList` (line 209-218). Concise and complete.

---

## 4. PR #1050 (strip per-cluster Envoy replicas)

### Summary

PR removes `clusters[i].loadBalancer.managed.replicas` precedence from `envoyReplicasForCluster` (renamed `envoyReplicas`), adds an admission validator that rejects the field when set, and updates the godoc on `ManagedLBConfig.Replicas` to call out uniformity.

### Findings

- ✅ The validator's error message correctly points users to the top-level field (`spec.clusters[%q].loadBalancer.managed.replicas is not supported; set spec.loadBalancer.managed.replicas at the top level instead`). Good.
- ✅ The renamed `envoyReplicas` correctly handles the nil chain: `if search.Spec.LoadBalancer != nil && search.Spec.LoadBalancer.Managed != nil && search.Spec.LoadBalancer.Managed.Replicas != nil`. No breakage when `LoadBalancer` is unset (single-cluster degenerate path returns `envoyReplicasDefault = 1`).
- 🟡 The field still exists structurally on `PerClusterLoadBalancerConfig.Managed` (because it shares `ManagedLBConfig` with the top-level path). PR keeps it but admission rejects setting it. Trade-off vs splitting into a `PerClusterManagedLBConfig` without `Replicas`:
  - Pro of admission-only approach (PR #1050's choice): no type fork, schema migration cost is zero, validator is symmetric with the rest of `validateClusters*`.
  - Pro of type split: API self-documents — IDEs would not autocomplete `replicas` under `clusters[].loadBalancer.managed`. Field nudges away the mistake before the user hits admission.
  - **My take: PR #1050's approach is correct.** Type split would force a CRD bump that breaks downstream tooling reading the OpenAPI schema. The admission rule is consistent with `validateMCMatchTagsNonEmpty` and `validateClustersNoRename` — admission is the chosen enforcement layer for this CRD. Add one comment on `PerClusterLoadBalancerConfig.Managed.Replicas` saying "Admission rejects this field; see validateClustersNoPerClusterEnvoyReplicas. Field exists structurally because Managed reuses ManagedLBConfig from the top level." Closes the API-honesty gap without the type fork.
- ✅ The godoc update at `ManagedLBConfig.Replicas` is accurate ("uniform across clusters"). One tiny improvement: end the godoc with a forward-pointer like the validator name, e.g. `... per-cluster overrides via spec.clusters[].loadBalancer.managed.replicas are NOT supported (admission rejects them — see validateClustersNoPerClusterEnvoyReplicas).` — already there, my apologies. The PR's already done this.

**PR #1050 verdict:** ready to merge. Merge order: after the B1 blocker fix, since #1050 touches the same `ensureDeployment` path and the merge will be cleaner. (Mechanically the diff is small and would rebase fine either way.)

---

## 5. Style / nits

### 🟡 N1 — Stale "(B16)" / "(B5)" comments slipped past simplify pass

`grep -n "(B16)\|(B14)\|(B18)\|(B5)\|(B8)\|(B9)\|(B3)\|(B4)\|(B13)\|(B1)" /tmp/gabase/` finds:

- `controllers/operator/mongodbsearchenvoy_controller.go:64` — "Cross-cluster enqueue labels..." (no B-tag, OK)
- ...

After running this grep, I find ZERO surviving `(B<n>)` references in code comments on ga-base. The simplify pass cleaned them. ✅

### 🟡 N2 — Dead struct field `clusterWorkItem` is single-field

`controllers/operator/mongodbsearchenvoy_controller.go:198-200`:

```go
type clusterWorkItem struct {
    ClusterName string
}
```

Phase 2 will likely add fields (per-cluster client, plan unit, etc.). Today it's a one-field struct. The simplify pass commit message explicitly called this out as "deliberately not changed" — reasonable, since `wl[i].ClusterName` is more readable than `wl[i]`.

✅ Acknowledge as kept by design.

### 🟡 N3 — `selectEnvoyClient` and `SelectClusterClient` are near-duplicates with subtly different fallback semantics

- `controllers/operator/mongodbsearchenvoy_controller.go:693-701` (`selectEnvoyClient`): unknown clusterName → falls back to `central` silently. The reconcile loop is responsible for surfacing it as Pending in per-cluster status.
- `controllers/searchcontroller/cluster_clients.go:17-29` (`SelectClusterClient`): unknown clusterName → returns `(nil, false)`. The caller MUST check the bool.

Two policies for the same problem. The Envoy controller's silent-fallback is convenient (it always gets a working client), but it means a typo in `clusterName` will silently land Envoy resources in the central cluster. The search controller's `SelectClusterClient` is stricter and surfaces the gap.

For ga-base, keep both but document the divergence in their godocs:

- `selectEnvoyClient`: "Silent central-fallback on unknown name. The reconcile loop surfaces unknown ClusterNames as Pending earlier in the call chain (see reconcileForCluster:231-235)."
- `SelectClusterClient`: "Returns (nil, false) on unknown name — caller MUST check. Used by the search controller's per-cluster fan-out where strictness is a feature."

Reconvene at Phase 2 to decide whether to converge.

### 🟡 N4 — The `// TODO: can we find a better cleanup mechanism` at envoy_controller.go:126

```go
// TODO: can we find a better cleanup mechanism, and optimize the watching of the loadbalancer field by this controller ?
```

Stale-feeling but useful as a placeholder. Either rewrite as a JIRA-style ticket reference or fold into the `Risks & open items` of the spec.

---

## 6. Docs

### 🟡 D1 — Spec divergence: `ProxyServiceNamespacedNameForCluster(clusterIndex int)` is **not** in ga-base

Spec line 149 says:

> Per-cluster mongot ConfigMap... — Phase 2 net-new — same gap as the StatefulSet; Phase 2 renders one per cluster from top-level `external.hostAndPorts`

And further: "today `ProxyServiceNamespacedName()` at `api/v1/search/mongodbsearch_types.go:497-499` hard-codes `0` as the index". On ga-base, the file moved to lines 564-566 but the hard-coded `0` is still there. This matches the spec's intent (Phase 2 adds the per-cluster variant). ✅ no action.

### 🟡 D2 — Spec line 39-73 ("Routing strategy") says mongot→mongod sync may cross clusters; code has no comment about this

Covered in §2 above. Not a doc divergence per se — the spec is right, the code just doesn't yet have the corresponding inline comment because that rendering hasn't landed (Phase 2). Note for Phase 2 reviewers.

### ✅ Sound — generated CRDs match types

`make manifests` on ga-base produces zero diff. Hand-verified `git diff config/crd/bases/ helm_chart/crds/ public/crds.yaml` after regeneration. CRD shape (deprecation markers, MaxItems, XValidations) is consistent with the type definitions.

---

## 7. Risk / unknowns

### 🟡 R1 — `buildProxyService` selector uses single-cluster Envoy Deployment name (not a ga-base blocker, but Phase 2 trip-wire)

`controllers/searchcontroller/mongodbsearch_reconcile_helper.go:808-814`:

```go
if search.IsLBModeManaged() && search.IsLoadBalancerReady() {
    selector = map[string]string{appLabelKey: search.LoadBalancerDeploymentName()}
} else {
    selector = map[string]string{appLabelKey: unit.podLabels[appLabelKey]}
}
```

In single-cluster (current ga-base scope), this is correct — there's exactly one Envoy Deployment with the unsuffixed name. In MC mode, when Phase 2 fans out per-cluster proxy Services across member clusters, each member-cluster's proxy svc selector must point to **that cluster's** Envoy Deployment name (`LoadBalancerDeploymentNameForCluster(clusterName)`), not the unsuffixed name.

ga-base doesn't run this code in MC mode (helper is single-cluster only), so it's not a blocker. Phase 2 must update this. Add a `// TODO(phase-2): when per-cluster fan-out lands, the selector must use LoadBalancerDeploymentNameForCluster(unit.clusterName).` to flag the trip-wire.

### 🟡 R2 — Phase 2 in-flight scaffolding: any `clusterIndex` field that ga-base assumes Phase 2 will fill?

Reviewed `reconcileUnit` struct (`controllers/searchcontroller/mongodbsearch_reconcile_helper.go:103-115`). No `clusterIndex` field, no `clusterName` field. The struct is shard-aware via `logFields []any` (`shard, name, shardIdx, idx`) but cluster-blind. ga-base does NOT pre-assume Phase 2 fills cluster fields — the unit struct is single-cluster as-is, and Phase 2 will add the cluster fields when fan-out lands.

✅ No latent inconsistency. ga-base is internally consistent at single-cluster scope.

### 🟡 R3 — Cross-controller status race

Search controller patches `status.{phase, message, version, warnings, clusterStatusList, ...}` via `commoncontroller.UpdateStatus(ctx, r.client, r.mdbSearch, workflowStatus, log)` (`mongodbsearch_reconcile_helper.go:219-221`).

Envoy controller patches `status.loadBalancer` via `commoncontroller.UpdateStatus(ctx, r.kubeClient, search, st, log, partOption)` where `partOption := searchv1.NewSearchPartOption(SearchPartLoadBalancer)` (`mongodbsearchenvoy_controller.go:277-280`). The `partOption` directs the JSON Patch at `/status/loadBalancer` only.

Two controllers, two paths, JSON-patch-targeted at non-overlapping subtrees. **No stomp risk** as long as `UpdateStatus` honors the `partOption` and renders a JSONPatch with `path: "/status/loadBalancer"` exclusively. Verified in `GetStatus` / `GetStatusPath` (`mongodbsearch_types.go:505-521`) — the partOption returns `s.Status.LoadBalancer` and path `/status/loadBalancer`. ✅

There's no test that runs both controllers concurrently against the same fake client and asserts neither's update is lost. Adding one would close the formal verification gap, but it's a 🟡 (the design is right; risk is low).

### 🟡 R4 — Empty cluster list edge case

`AggregateClusterStatuses` (`api/v1/search/status_aggregation.go:54-67`):

```go
if len(items) == 0 {
    return
}
```

When `items` is empty (single-cluster legacy), no-op. ✅

But what about `len(items) > 0 && all items have empty Phase`? `WorstOfPhase("","","")` returns `""` (the empty string), and `if worst := WorstOfPhase(phases...); worst != ""` skips the Phase write. So the top-level Phase is preserved as whatever the workflow set it to. ✅

This is correct, but the path is undocumented. Add a comment at line 64:

> // Empty WorstOfPhase result (all per-cluster phases were unset/unknown) leaves
> // the top-level Phase as-set by the workflow status — caller's existing semantics.

---

## Output line

`RESULT: review-doc-pushed docs/dev/reviews/2026-05-03-search-ga-base-review.md`
