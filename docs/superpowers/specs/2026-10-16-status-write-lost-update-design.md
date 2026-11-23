# Status write: fix the lost update caused by stale cached reads

Date: 2026-10-16. Status: draft for review. Nothing here is implemented.

## 1. Problem

`pkg/kube/commoncontroller/resourcestatus.go` `UpdateStatus` mutates the in-memory custom resource (read from the informer cache) and then sends a JSON-patch `replace` of the **whole** status path with that in-memory copy (`patchUpdateStatus`). If the cached copy is older than what the operator itself wrote earlier, the patch writes the old values back and silently reverts the newer ones. This is a lost update.

Observed failure (`e2e_replica_set`, task `..._dfdcdda5ba..._26_10_14_09_10_33`, execution 0, operator log):

| Time (UTC) | Event |
|---|---|
| 09:31:59.990 | Status patched to `Running`, `members: 3` |
| 09:31:59.996 | Next reconcile starts and reads the cached CR: `phase: Pending`, no `members` |
| 09:32:00.970 | That reconcile patches `Pending` with its stale in-memory status. `members` is dropped on the server |
| 09:32:29.852 | `Running`, `members: 3` written again |
| 09:32:29.858 | Next reconcile starts. Its cached status has no `members`, spec has `members: 1` |
| 09:32:33 | `Status.Members == 0` makes `scale.ReplicasThisReconciliation` treat the scale-down as a fresh create, so it removes two voting members at once. Ops Manager returns 400 |

Which event started the reconcile at 09:31:59.996 was not identified. The watch predicates ignore status-only changes (section 3.4), so it was not the status write itself. The stale read does not depend on the trigger.

## 2. Goals, non-goals, anti-goals

Goals
- A status write never reverts a status field that the writing reconcile did not intend to change.
- Correct with a lagging cache and with several controllers writing the same CR.
- One change in the shared write path, covering all CR kinds, that preserves every existing status semantic.

Non-goals
- Making decision inputs read from status always fresh. See 6.4.
- Replacing the informer cache for reads in reconcilers.
- Upgrading controller-runtime.

Anti-goals
- A fix that only works for the replica set controller.
- An extra uncached API call on every status write in the common (non-stale) case.
- Changing the shape of any CR status or CRD.

## 3. Findings

### 3.1 `GetAPIReader()` and uncached clients

`GetAPIReader()` is used only for telemetry: `pkg/telemetry/collector.go:177,186` and `pkg/telemetry/cluster.go:49` (reads the kube-system namespace UID, central and member clusters). **No reconciler uses it.** Other uncached clients (`client.New`) exist only at startup (`main.go:229,510,549`).

All reconcilers use `mgr.GetClient()`, the cached client (`main.go`). The only type excluded from the cache is `batchv1.Job` (`main.go:190`). Member clusters are created with `runtime_cluster.New` and a cached client; no CRs are read from them.

### 3.2 Status writers

All CR kinds write status through `commoncontroller.UpdateStatus` (about 170 call sites). Kinds with a status writer: MongoDB, MongoDBMultiCluster, MongoDBOpsManager (sub-paths `/status/opsManager`, `/status/applicationDatabase`, `/status/backup`), MongoDBUser, MongoDBSearch (`/status`, `/status/loadBalancer`, `/status/metricsForwarder`), VoyageAI. `ClusterMongoDBRole` has no status.

Concurrent-writer exposure:

| Kind | Writers | Exposure |
|---|---|---|
| MongoDBSearch | Three controllers on the same CR (main, envoy, metrics) | Highest. The main controller's `/status` replace can overwrite `loadBalancer` and `metricsForwarder` with cached stale data |
| OpsManager | OM and AppDB parts in one reconcile, separate sub-paths | Stale-prone across reconciles |
| Others | Single reconciler | Self-race through the stale cache (this bug) |

### 3.3 Status read as decision input

- Replica set: `Status.Members` drives scale-down (`mongodbreplicaset_controller.go:143`), also `:1227`, `:266`, `:339`.
- Sharded: `SizeStatusInClusters`, `ShardCount`, `Phase`, `LastTransition`. A ConfigMap state store exists, but it is read through the cached client.
- OpsManager and AppDB: `Status.OpsManagerStatus.Version`, `Status.AppDbStatus.Members` (`appdb_scaler.go:41`).
- MRS, backup, user: `Phase`, `LastTransition`, `Username`, `Database`.

### 3.4 Re-enqueue behaviour

- Predicates for MongoDB, OpsManager and User return true only when the status is **unchanged** (`reflect.DeepEqual(old.GetStatus(), new.GetStatus())`, `controllers/operator/watch/predicates.go`). Status-only updates are dropped, and updates that touch only the `LastAchievedSpec` annotation are dropped.
- MongoDBSearch envoy and metrics controllers and the VoyageAI controller have no predicate, so any write to the CR (including status) re-enqueues them.
- `MaxConcurrentReconciles` defaults to 1 per controller. Two controllers on the same CR still run in parallel.

### 3.5 Other facts that shape the design

- No `resourceVersion` / optimistic lock is used for CR writes anywhere.
- `patchUpdateStatus` returns `nil` for any error other than `Invalid`. A conflict or transport error on the status write is silently dropped (`resourcestatus.go`, after the `IsInvalid` branch). This is a latent bug independent of the stale reads.
- `ensureStatusSubresourceExists` exists only because JSON-patch `replace`/`add` fail when a parent path is missing.
- Annotations are patched separately and do not touch status (`pkg/kube/annotations`). Finalizers use whole-object `Update` (user and search metrics controllers), which already carries the cached `resourceVersion`.
- controller-runtime is v0.21.0, client-go and apimachinery v0.34.12 (`go.mod`).
- Test tooling: the controller-runtime fake client supports JSON and merge patch on the status subresource and `Status().Update`. `interceptor` is widely used and can simulate a stale or conflicting write. `test/envtest` provides a real API server.

## 4. What the community recommends

Official:
- controller-runtime FAQ: assume the cache is eventually consistent and make the reconcile "enforce the entire state of the world each time". Mitigate with optimistic locking; where that isn't possible, track what you did and requeue (ReplicaSet controller). (controller-runtime `FAQ.md`)
- API conventions: `/status` subresource; status is "most recent observations"; history such as last-transition time is "not guaranteed to not be lost"; large or fast-changing status belongs in separate objects. (kubernetes/community `api-conventions.md`)
- `client.MergeFromWithOptimisticLock` puts `metadata.resourceVersion` in the patch so the server returns 409 if the object moved. Merge patch replaces lists wholesale. Strategic merge patch is not supported for CRDs. (controller-runtime `pkg/client/patch.go`)
- Server-side apply is the recommended controller pattern, but "the applied object must contain all the fields that the controller cares about", fields omitted from the next apply are removed, and CRD lists are atomic unless marked. (kubernetes.io server-side-apply docs)
- Upstream is moving to read-your-writes: KEP-5647 "Stale Controller Detection and Mitigation" (tracks last-written resourceVersion; alpha, planned for 1.36) and controller-runtime issue #3320 ("ReadYourWritesConsistency", implementation under v0.25 per the issue). KEP-2340 (consistent reads from cache) is server-side LIST only and does not help client informers.

Practice: Cluster API's patch helper patches status with a merge patch of the diff against the object it read, without an optimistic lock (it takes the lock only for conditions).

### 4.1 Verification of the earlier unverified items (2026-10-16)

| Item | Result | Evidence |
|---|---|---|
| controller-runtime read-your-writes client | Confirmed, experimental, opt-in, **not usable here** | First released in v0.25.0 (2026-09-03). `client.Options.Cache.EnableReadYourWritesConsistency` (default false): reads block until the informer has seen the client's own earlier writes. Covers `Status().Patch` and `Status().Update` and merge patches. Needs controller-runtime v0.25+, k8s.io/* v0.37, Go 1.26; no backport. Docs: "does not work correctly when a resource has more than one Namespace configured" (issue #3607). MCK builds a multi-namespace cache (`main.go:198-208`), so this limitation applies directly. Only writes made through the same client are tracked, and it does not turn optimistic-lock conflicts into client-side failures |
| "KEP-5505" | Number was the PR; the KEP is **5504 Comparable Resource Version**, stable in Kubernetes v1.35. Guarantees RVs are monotonically increasing integers for conformant types and adds a client-go compare helper. Aggregated apiservers may not conform |
| KEP-5647 stale controller handling | Beta in 1.36 per the tracking issue (`kep.yaml` says beta, latest milestone v1.37). Targets kube-controller-manager controllers. Whether client-go exposes it to third parties was not verified. Nothing an operator can rely on |
| KEP-2340 | Stable in v1.34, server-side LIST only. Irrelevant to informer lag |
| Kubebuilder book, Operator SDK | **No guidance** on stale cache or status conflicts. Operator SDK says split-client reads are "eventually consistent" and suggests building your own client for API-server reads. Kubebuilder scaffold advice: "Re-fetch before updates" |
| `Status().Patch` response | Confirmed: the server response is decoded into the passed object for every patch type, so the in-memory object (status, spec, `resourceVersion`) is replaced with server state after a successful patch |
| resourceVersion in a status write | Confirmed: honoured as a precondition on `/status` for PUT and for patches; mismatch returns 409 |
| cert-manager | Server-side apply of only its own condition when the feature gate is on, otherwise `UpdateStatus`; relies on workqueue requeue for conflicts |
| Crossplane | `Status().Update` and treats 409 as a silent requeue (`SilentlyRequeueOnConflict`); a code comment says they "_want_ the update to fail if we get a 409 due to a stale version" |

Envtest spike against a real kube-apiserver 1.35 and the MongoDB CRD (throwaway test, deleted), writing a Running/`members: 3` status with a stale or current `resourceVersion`:

| Write | Stale version | Current version |
|---|---|---|
| JSON patch: `test /metadata/resourceVersion` then `replace /status` | Rejected, **422 Invalid** (not 409), message "the server rejected our request due to an error in our request"; server status unchanged | Applied |
| Merge patch with `MergeFromWithOptimisticLock` | **409 Conflict** | not tested |
| PUT (`Status().Update`) | **409 Conflict** | Applied |

So a JSON patch does carry a working version check, but a mismatch comes back as 422 Invalid. Two consequences: callers cannot use `apierrors.IsConflict`, and the existing `patchUpdateStatus` treats `IsInvalid` as "status subresource missing", so it would run its bootstrap and retry instead of surfacing the conflict. PUT and the locked merge patch return a clean 409.

Still unverified: which client-go release ships the RV compare helper, and whether client-go exposes the KEP-5647 bookmark API to third parties.

Spike on the fake client (throwaway test, deleted): a merge patch built with `MergeFromWithOptions(base, MergeFromWithOptimisticLock{})` from a stale base returns the conflict error `object was modified`; the same diff without the lock applies and keeps `members` on the server; clearing a field emits `null` for exactly the keys present in the base (`{"status":{"members":null,"warnings":null}}`); after a successful patch the in-memory object has the server's `members`. The fake client therefore enforces `resourceVersion` in patches, so conflict paths can be unit-tested without envtest.

## 5. Options considered

| Option | Fixes lost update | Why / why not |
|---|---|---|
| A. Merge patch of only the changed fields (diff vs the object read), no lock | Partly | A diff against a stale base can skip a write: the cache says `Pending`, the server says `Running`, the reconcile wants `Pending`, the diff is empty and the server keeps `Running`. Also no protection between writers |
| B. Optimistic lock (`resourceVersion`) with requeue on 409 | Yes | Stale cached RV yields repeated 409s until the cache catches up, wasting reconciles, unless a fresh read is used on conflict |
| C. Uncached read before every status write | Needs B | Always costs one quorum read per status write, and does not protect between the read and the write |
| D. Server-side apply on `/status` | Yes, for fully declared fields | Poor fit: status is written incrementally (for example `members` only on `Running`). Omitting a field the same manager applied before removes it, so each write would need the complete current value of every owned field, which is the data we cannot trust. Per-controller field managers also need CRD list markers |
| E. Skip reconcile until the cache has seen our write (expectations / read-your-writes client) | No (reads only) | Fixes the stale decision, not the overwrite, and is not available in controller-runtime v0.21 |
| F. Keep decision state out of status | No (reads only) | Right long-term, large migration. See 6.4 |
| G. Keep the whole-status write, add the `resourceVersion` precondition, requeue on 409 | Yes | Smallest change; keeps every existing status semantic. This is the recommendation (section 6) |

## 6. Recommended design

### 6.1 Why the earlier design (diff patch plus replay on a live copy) is rejected

An audit of every in-memory status edit (hits listed in appendix A) shows a diff-and-replay design is not safe without a large rework:
- Warnings are reset, re-added by validation, and appended again inside `UpdateStatus`, all relative to different bases. A diff against a base captured just before `UpdateStatus` drops the reset and the validation warnings, and a replay on a live object duplicates or loses them.
- The sharded cluster controller replaces the CR's whole in-memory status with the state ConfigMap's status (`mongodbshardedcluster_controller.go:748`) before anything is written. A diff against a base captured later never persists that restore.
- `UpdateStatus` reads the existing status to decide what to write (`LastTransition`, PVC merge, `priorStatusMembers`, `priorK8sMemberCount`, migration conditions). A replay on a different object gives different results.
- Search `MetricsForwarder = nil` followed by `UpdateStatus(OK, part)` recreates `Running` instead of clearing, so the diff semantics would change.
- The design needs a snapshot taken at read time that survives across several `UpdateStatus` calls in one reconcile, which the current signatures cannot carry.

### 6.2 Recommended: keep the whole-status write, add a `resourceVersion` precondition

Change only the write, not what is written:
1. The write sends the same status as today, but with the in-memory object's `resourceVersion` as a precondition. Use `Status().Update` (PUT): the envtest spike showed it returns a clean 409 on a stale version. A JSON patch with a `test` op also enforces the version but returns 422 Invalid, which collides with the existing `IsInvalid` bootstrap handling.
2. A stale in-memory object (the cache lagged) is rejected by the server with 409. The reconcile is requeued and the next one reads a fresher object. Nothing stale is ever written.
3. A successful write refreshes the in-memory object from the server response (controller-runtime decodes it into the passed object), so the next write in the same reconcile has the new version.
4. Return the error for every failure. The silent `return nil` goes away. A 409 is converted at the top of each `Reconcile` into a quiet requeue (the Crossplane pattern), so logs do not fill with conflict errors.

Why this is right for MCK
- It preserves every existing status semantic, including the warnings flow, the sharded state restore, `LastTransition` and the migration conditions, because what is written is unchanged. Only writes built on an outdated object are refused.
- It fixes the reported bug: the reconcile that read the stale `Pending` status is rejected instead of dropping `members`.
- It follows the guidance verified in section 4: optimistic concurrency plus requeue on conflict (controller-runtime FAQ, Crossplane, Kubebuilder "re-fetch before updates").
- It needs no `APIReader`, no diff, no replay, and no controller-runtime upgrade.
- It also stops the Search main controller from clobbering `loadBalancer` and `metricsForwarder`, since its stale whole-status write is now refused.

### 6.3 Prerequisite: every write of the reconcile carries the version (the version chain)

The resource version is a property of the whole object, so any other write to the CR moves it. A reconcile that writes the CR more than once is safe only if it forms a **chain**: every write carries the in-memory `resourceVersion` as a precondition, and the in-memory version is refreshed from that write's response. The first write that finds the in-memory object stale returns 409 and nothing stale is persisted.

An **unlocked write breaks the chain in either direction**:
- If the in-memory version is not refreshed after it, the next locked write conflicts with our own earlier write. This is what `annotations.SetAnnotations` does today (it patches a freshly read copy without a version and copies back only the annotations), so almost every reconcile, which saves `LastAchievedSpec` and then writes status, would conflict on every success.
- If the in-memory version **is** refreshed from its response, the next status write carries a fresh version with a stale in-memory status and passes the precondition. Envtest on kube-apiserver 1.35 (section 4.1, throwaway test) with a stale in-memory object: an unlocked annotation patch plus a refreshed version, then a status PUT, applied and left `members: 0` on the server (the original bug). A merge patch of the annotations locked on the in-memory version returned 409 on the stale object, left `members: 3`, and the following status PUT also returned 409; on a current object both succeeded.

Decision: add a locked annotation helper, used only by controllers converted to the new status write.
- It builds a merge patch of the annotation changes on a **copy** of the in-memory object, locked on the in-memory `resourceVersion` (`MergeFromWithOptions` with `MergeFromWithOptimisticLock`), so a stale object returns 409.
- Only on success does it copy the annotations and the new `resourceVersion` back onto the in-memory object. It must not decode the response into the in-memory object itself, which would overwrite status edits made earlier in the reconcile (warnings reset, validation warnings).
- The reconcile order does not change: annotations, connection string secret, final status.
- The shared `SetAnnotations` and `RemoveAnnotation` stay as they are. Refreshing a version from them is unsafe for the User controller, which does a whole-object `Update` after `SetAnnotations`.
- Any other CR write in a converted controller (finalizers, other annotation helpers) needs the same treatment: locked on the in-memory version, version refreshed from the response, or moved after the final status write.

### 6.4 Consequences and limits

- A conflict wastes one reconcile (milliseconds of cache lag, requeued with backoff). Side effects done before the refused write (Ops Manager updates, StatefulSet changes) are idempotent today and are simply done again.
- Stale reads used as input are not fixed by this. Before this change a decision made on stale status could be persisted over newer status. After it, the persisted write is refused. A stale input can still drive one idempotent step (for example scale-down repeating the previous step).
- Moving decision state to reconstructable sources (StatefulSet replicas, the state ConfigMap) remains a separate effort.
- The read-your-writes client would remove most conflicts but needs controller-runtime v0.25, k8s.io/* v0.37, Go 1.26, and does not work with a multi-namespace cache. Revisit after an upgrade; it is complementary, not required.

## 7. Effect on the open PRs

- **#1715** (replica set scale-down falls back to StatefulSet replicas when `Status.Members == 0`): becomes a redundant defence once the write path no longer drops `members`. Decision (2026-10-16): #1715 was closed without merging; the fallback is not part of the product code. The pilot (#1721) carries the regression test of the section 1 sequence (a status write built on a stale object is refused and `members` survives). The pilot has not run in CI yet.
- **#1716** (AppDB 401 early return): unrelated to stale status, stays needed.
- **#1717** (test fixes): unrelated, stays needed.

## 8. Test plan

- Unit, fake client: a status write with an outdated `resourceVersion` returns a conflict and leaves the server status untouched; a write with the current version succeeds and refreshes the in-memory object.
- Unit: the reported sequence. Write `Running`, `members: 3`; start a second writer from the pre-write object; its write is refused; after a re-read it writes `Pending` and `members` is still 3. This replaces `TestScalingDownScalesOneMemberAtATime_WhenStatusMembersIsStale`.
- Unit: `SetAnnotations` followed by a status write in the same reconcile succeeds (this fails without 6.3).
- Unit per kind: every controller's final status write after its annotation or finalizer writes succeeds with a current version (RS, standalone, sharded, MRS, OM, user, search, VoyageAI).
- Unit: Search main and envoy writers do not overwrite each other.
- Unit: non-conflict errors are returned, not swallowed; a conflict becomes a quiet requeue at the `Reconcile` entry.
- e2e: the previously flaky tasks (`e2e_replica_set`, `e2e_om_appdb_scram`, search tasks) plus a broad light label set, since every controller changes. Track conflict frequency in operator logs.

## 9. Risks and open points

- Chain breaks (6.3): a CR write that is unlocked or does not refresh the in-memory version either self-conflicts or defeats the lock. The most important risk; the per-kind unit tests, including one that runs a full stale reconcile through the annotation write, are the guard.
- Reconcile churn: a conflict requeues the reconcile. There is no hard rate gate; review the conflict rate in CI logs after the first runs.
- Surfacing previously swallowed errors may expose latent failures in tests that relied on them.
- Controllers without a status predicate (Search envoy and metrics, VoyageAI) re-enqueue on any write, including the refused-then-retried ones. A refused write changes nothing on the server and does not re-enqueue; a retried successful write is the same event as today.
- Separate, existing bug to confirm and fix on its own: Search `MetricsForwarder = nil` followed by `UpdateStatus(OK, part=MetricsForwarder)` (`mongodbsearch_metrics_controller.go:248,1396`) recreates `Running` and never clears. Not verified at runtime.

## 10. Left to the builder

- Where the conflict-to-requeue conversion lives (per controller `Reconcile`, or a wrapper).
- Whether to land all kinds in one change or pilot on the MongoDB replica set first.
- Verifying the two remaining unverified items in 4.1 if a controller-runtime upgrade is planned.

## Appendix A. In-memory status edits outside `UpdateStatus` (audit)

Class A means the edit reaches the server only because the whole-status write carries it.

| Site | Edit | Class |
|---|---|---|
| `common_controller.go:383` and `mongodbopsmanager_controller.go:780` | Reset `Warnings` to empty after the read | A |
| `mongodb_validation.go:907`, `mongodbmulti_validation.go:38,116`, `opsmanager_validation.go:372`, `opsmanager_types.go:863` | `AddWarningIfNotExists` during validation | A |
| `mongodbshardedcluster_controller.go:748` | `sc.Status = *deploymentState.Status` (whole status restored from the state ConfigMap) | A, major |
| `mongodbmultireplicaset_controller.go:245` | `FeatureCompatibilityVersion` set mid-reconcile | A, low impact |
| `mongodbsearch_metrics_controller.go:248,1396` | `MetricsForwarder = nil` then `UpdateStatus(OK)` recreates `Running` | A, likely existing bug |
| `mongodbsearchenvoy_controller.go:350` | `LoadBalancer = nil`, followed by an explicit null patch | B |
| `mongodbshardedcluster_controller.go:816-831, 2692-2713, 136-141` | State ConfigMap copies of status | C and B |

`UpdateStatus` options: all REPLACE except `Warnings` (APPEND), `PVCs` (MERGE or CLEAR), `LastTransition` (set only when `Common` changed), the sharded size options (skipped when nil, never clear), the migration conditions (conditional on phase and `spec` external members), and the non-internal AppDB branch (resets the whole `AppDbStatus`). Several read the existing in-memory status (`priorStatusMembers`, `priorK8sMemberCount`, the migration conditions, `UpdateCommonFields`). These reads are the reason a replay on a live object differs, and the reason 6.2 keeps the write unchanged.
