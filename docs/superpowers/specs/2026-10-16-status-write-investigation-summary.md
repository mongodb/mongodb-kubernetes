# Stale status writes and flaky e2e tests: investigation summary and resume guide

Date: 2026-10-16. Status: paused. This file is the single entry point for resuming. The detailed design is in `2026-10-16-status-write-lost-update-design.md` (same directory).

## 1. How this started

A weekly report of the flakiest MCK e2e tasks led to a series of fixes. Test-only fixes and root-caused operator bugs were split into separate PRs. The last open thread is a systemic one: **status writes built on a lagging informer cache overwrite newer status** (the "lost update"), which caused the `e2e_replica_set` scale-down failure and can affect every controller.

## 2. Map of work (PRs, branches, worktrees, patches)

| What | Branch / PR | State |
|---|---|---|
| Timeouts, stale user secret wait, `om_request` read-timeout retry, race operator memory | PR #1709 | merged |
| Shard removal leaves an orphaned replica set (KUBE-519) | PR #1710, `maciejk/fix-shard-removal-orphan-replica-set` | open, review requested, patches green (21/21) |
| Replica set scale-down falls back to StatefulSet replicas when `Status.Members == 0` | PR #1715, `maciejk/fix-rs-scale-stale-status` | draft |
| AppDB reconcile continues on Ops Manager 401 after a password change | PR #1716, `maciejk/fix-appdb-401-deadlock` | draft |
| Test fixes: RS scram stale secret wait, search x509 mongod params wait | PR #1717, `maciejk/fix-flaky-master-tests-2` | draft, `skip-changelog` |
| Systemic fix pilot (this PR): optimistic status write for the replica set controller, plus the design docs | `maciejk/status-write-optimistic-pilot` | draft, nothing run in CI yet |

Evergreen patches submitted (priority 40), results not yet collected at pause time:
- RS scram + search x509 test fixes: https://spruce.corp.mongodb.com/version/6ad0ffde22bcdf0007fd5e54
- AppDB 401 fix (`e2e_om_appdb_scram`): https://spruce.corp.mongodb.com/version/6ad100f25a7579000724ec77
- RS scale-down stale status fix (`e2e_replica_set`): https://spruce.corp.mongodb.com/version/6ad100fd74b9bd00072a89d6

Local worktrees (siblings of `mongodb-kubernetes`, may need `git worktree prune` if removed): `mck-rs-scale`, `mck-appdb-401`, `mck-status-design` (design branch `maciejk/status-write-design`, local only, its commits are copied into this PR), `mck-status-pilot`. The `mongodb-kubernetes` checkout is on `maciejk/fix-flaky-master-tests-2`.

## 3. Flaky test investigation results (30 mainline versions, top flakes)

Sample sizes are small (1 to 12 failed runs per task). `flaky_aggregate` could not fetch about 20 of 30 versions in some runs. The devprod MCP rate limit (about an hour window) pushed most queries to the `evg` CLI.

| Task | Finding | Status |
|---|---|---|
| `e2e_search_mongot_replicaset_x509_auth` | Race: `createSearchIndexes` ran before agents applied `mongotHost` (`SearchNotEnabled`, code 31082). MongoDBSearch Running only means mongot is ready; the source MongoDB was already Running | fixed in #1717 (wait for mongod parameters). A second cluster, a 300s timeout in `test_create_database_resource`, is not analysed |
| `e2e_replica_set_scram_sha_256_user_connectivity` | 1 of 8 failures was the stale connection-string secret (same as the sharded sibling fixed in #1709). The rest are OpenShift phase-wait timeouts (400/600s, 150s) with a `FailedMount` on the group secret, and one `Operator hasn't started after 60 retries` | stale secret fixed in #1717. OpenShift timeouts: environment, no confirmed cause |
| `e2e_om_appdb_scram` | After an AppDB password change OM returns 401, the AppDB reconcile returned early with OM `Failed`, so the new password was never published and OM never rolled. Also a dead `err` check mislabelled the failure (`appdbreplicaset_controller.go` ~1993) | fixed in #1716, e2e pending. Why the first 401 happens is not confirmed |
| `e2e_replica_set` | Scale 3 to 1 rejected by OM (`Cannot add/remove multiple voting members`). Root cause is the lost status update, section 4 | symptom fix in #1715, systemic fix is this pilot |
| `e2e_tls_sharded_cluster_certs_prefix` | 12 failures: 6 `test_rotate_tls_certificate` 800s timeouts (no state transition, cause unknown; commit `5081f278c` notes rotation does not always show a phase change), 5 fast OM project-create errors (401 `NOT_ORG_GROUP_CREATOR`, 401 `USER_CANNOT_ACCESS_GROUP`, 409 `GROUP_ALREADY_EXISTS`), 1 OM 401 `UNEXPECTED_ERROR` | not investigated further |

Other open items: OpenShift CPU capacity ("Insufficient cpu"); the unexplained 610s `test_create_sharded_cluster` failure on 2026-10-09; cloud-qa returns spurious 401 `USER_CANNOT_ACCESS_GROUP` on its own, so the digest-replay explanation in the #1709 description for an OpenShift 401 is overstated (a wording follow-up, not done); whether to add `USER_CANNOT_ACCESS_GROUP` to the transient messages in `assert_reaches_phase` (decision pending, the user wants root cause first).

## 4. Root cause of the lost status update (confirmed from the operator log)

Task `mongodb_kubernetes_e2e_mdb_kind_ubi_cloudqa_e2e_replica_set_dfdcdda5ba68e2326513df0c1405be6af0f83e83_26_10_14_09_10_33`, execution 0 (artifacts may still be under `~/.local/share/uv/tools/core-platforms-ai-tools/lib/python3.13/site-packages/tmp/evergreen_artifacts/dfdcdda5ba68/`):

1. 09:31:59.990 status patched to `Running`, `members: 3`.
2. 09:31:59.996 the next reconcile reads the cached CR: `phase: Pending`, no `members` (6ms later).
3. 09:32:00.970 that reconcile patches its stale in-memory status back. `pkg/kube/commoncontroller/resourcestatus.go` sends one JSON-patch `replace` of the whole `/status` with no `resourceVersion` check, so `members` is dropped on the server.
4. The test then sets `members: 1`. With `Status.Members == 0`, `scale.ReplicasThisReconciliation` treats the scale-down as a fresh create and removes two voting members at once. OM returns 400.

What started the reconcile at step 2 was not identified. The watch predicates drop status-only changes, so it was not the status write itself; the stale read does not depend on the trigger. An earlier suggestion that the cache lagged "more than 30s" was wrong.

## 5. Facts established about the codebase

- `GetAPIReader()` is used only by telemetry (`pkg/telemetry/collector.go:177,186`, `pkg/telemetry/cluster.go:49`). All reconcilers use the cached `mgr.GetClient()`. Only `batchv1.Job` is excluded from the cache (`main.go:190`). The cache is multi-namespace when watched namespaces are configured (`main.go:198-208`). Member clusters use cached clients and hold no CRs.
- All CR kinds write status through `commoncontroller.UpdateStatus` (about 170 call sites). Highest concurrent-writer exposure: MongoDBSearch (three controllers on one CR: main, envoy, metrics). OpsManager writes sub-paths `/status/opsManager`, `/status/applicationDatabase`, `/status/backup`.
- No `resourceVersion` or optimistic lock is used for CR writes anywhere. `patchUpdateStatus` returns nil for any error other than `Invalid` (a failed status write is silently dropped).
- Predicates for MongoDB, OpsManager and User drop status-only updates; Search envoy, Search metrics and VoyageAI controllers have no predicate. `MaxConcurrentReconciles` defaults to 1 per controller.
- The status is also read as decision input (replica set `Status.Members`, sharded `SizeStatusInClusters` and `ShardCount`, OM `Status.OpsManagerStatus.Version`, AppDB `Status.AppDbStatus.Members`, and others). Details in the design doc.
- The audit of in-memory status edits outside `UpdateStatus` (appendix A of the design doc) found warnings, the sharded `sc.Status = *deploymentState.Status` restore, and the Search `MetricsForwarder = nil` handling.

## 6. Community guidance (verified 2026-10-16)

- controller-runtime FAQ: assume the cache is eventually consistent; use optimistic locking and requeue. Official Kubebuilder and Operator SDK guidance on this is nearly absent ("re-fetch before updates").
- controller-runtime has an experimental read-your-writes client (`Client.Cache.EnableReadYourWritesConsistency`), first released in v0.25.0 (2026-09-03). It needs controller-runtime v0.25, k8s.io v0.37, Go 1.26, has no backport to our v0.21, and does not work correctly with a multi-namespace cache (issue #3607), which MCK uses.
- KEP-5504 (comparable resourceVersions, stable in 1.35; earlier references to "5505" were the PR number), KEP-5647 (stale controller handling, beta in 1.36, built-in controllers only), KEP-2340 (server-side LIST only). Not usable by us.
- Crossplane treats a 409 as a silent requeue; cert-manager applies only its own condition with server-side apply; Cluster API merge-patches a diff.
- Still unverified: which client-go release ships the resourceVersion compare helper; whether client-go exposes the KEP-5647 bookmark API to third parties.

## 7. Design decision and rejected options

Chosen: **keep what is written, add a `resourceVersion` precondition (PUT via `Status().Update`), requeue on 409.** A stale in-memory object gets 409 and nothing stale is persisted.

Rejected, and why:
- Diff merge patch plus replay on a live copy: breaks the warnings flow (reset, validation add, append), the sharded state restore, and replay-sensitive reads inside `UpdateStatus` (`LastTransition`, PVC merge, `priorStatusMembers`, migration conditions).
- Server-side apply on `/status`: omitting a field the manager applied before removes it, and our status is written incrementally.
- Upgrading to the read-your-writes client: needs controller-runtime v0.25, k8s v0.37, Go 1.26, and fails with a multi-namespace cache. Complementary later, not required.
- Plain JSON patch with a `test` op on `/metadata/resourceVersion`: works, but a stale version returns 422 Invalid (not 409), which collides with the existing `IsInvalid` bootstrap handling in `patchUpdateStatus`.

### Envtest results (real kube-apiserver 1.35, throwaway tests, deleted)

| Write | Stale version | Current version |
|---|---|---|
| JSON patch with `test /metadata/resourceVersion` then `replace /status` | 422 Invalid, server unchanged | applied |
| Merge patch with `MergeFromWithOptimisticLock` | 409 Conflict | not tested |
| PUT (`Status().Update`) | 409 Conflict | applied |

The version chain (design doc 6.3): a reconcile that writes the CR more than once is safe only if every write carries the in-memory `resourceVersion` and the in-memory version is refreshed from that write's response. Verified on envtest with a stale in-memory object and `members: 3` on the server:

| Annotation write | Following status PUT | Server `members` |
|---|---|---|
| Unlocked, version synced | applied | 0 (the original bug) |
| Locked on the in-memory version | 409 | 3 |
| Locked, current object | applied | 3 |

Reproducing the envtest checks: `make envtest-assets` (installs binaries into `bin/envtest`), then run a test package with `KUBEBUILDER_ASSETS=$PWD/bin/envtest/k8s/1.35.0-darwin-arm64` and a `TestMain` that calls `env.RunShared(m, env.WithCRDs("mongodb.com_mongodb.yaml"))` (see `api/mongodb/v1/mdb/mongodb_cel_envtest_test.go`).

## 8. The pilot in this PR (replica set controller only)

Changes:
- `pkg/kube/commoncontroller/resourcestatus.go`: `UpdateStatusOptimistic` (same mutation and log line as `UpdateStatus`, persisted with `Status().Update`; all errors returned).
- `pkg/kube/annotations/annotations.go`: `SetAnnotationsOptimistic` (merge patch on a copy of the in-memory object, locked on its `resourceVersion`; on success copies back only the annotations and the new version, never the response). `SetAnnotations` and `RemoveAnnotation` are unchanged; refreshing a version from them is unsafe for the User controller, which does a whole-object `Update` after `SetAnnotations`.
- `controllers/operator/mongodbreplicaset_controller.go`: `updateStatus` and `ensureBackupConfigurationAndUpdateStatus` overrides on `*ReconcileMongoDbReplicaSet`; the final annotation write uses the locked helper; a conflict returns as an error and the `Reconcile` defer turns it into a quiet `Requeue: true`. Reconcile order and `Failed` error handling are unchanged from master.
- `controllers/operator/workflow/{status,failed}.go`: new `Error() error` on the shared `Status` interface so a `Failed(conflict)` status is not persisted. **Needs a decision**, see section 10.

Tests: optimistic commoncontroller write tests; optimistic annotation tests (current object, stale object, in-memory status preserved, contrast with `SetAnnotations`); two replica set reconcile regressions (the incident sequence with scale-down 3 to 2 to 1 only after the conflict, and a stale reconcile that reaches the annotation write). The implementer reported the annotation regression fails when the locked helper is replaced by `SetAnnotations`; that swap was not re-run by the reviewer.

Verification already done: `go build ./...`, `go test ./pkg/kube/... ./controllers/operator/... ./controllers/om/...` pass with `GOTOOLCHAIN=go1.26.9`, `gofmt` clean, `golangci-lint` hooks pass. Hooks that need `.generated/context.export.env` could not run locally. Not done: any run on a real cluster or in CI.

History of the pilot, to avoid repeating mistakes: a first version synced the version from an unlocked annotation patch, which defeats the lock; the implementer worked around it by reordering the reconcile tail and changing error paths, which was rejected; the current version restores the order and locks the annotation write.

## 9. Behaviour changes to call out in review

- Any conflict returned from the replica set reconcile (including a conflict on a StatefulSet update) is now a quiet requeue, not a `Failed` status plus retry.
- A failed status write now returns an error instead of being silently dropped on the replica set path.
- The Search envoy, metrics and VoyageAI controllers (no predicate) re-enqueue on any write; a refused write changes nothing on the server and does not re-enqueue.

## 10. Open decisions

1. `Error() error` on the shared `workflow.Status` interface versus detecting a conflict at the few wrapping sites in the replica set controller. Interface change touches every controller's compilation.
2. One change for all kinds versus extending kind by kind after the pilot is green on `e2e_replica_set`. Recommendation: extend after CI is green.
3. Whether to remove the #1715 fallback in the write-path PR (design doc 7). The exit criterion proposed: remove it once this approach is green on `e2e_replica_set`.
4. Whether to land the controller-runtime upgrade (v0.25) later and enable read-your-writes as a complement.

## 11. Resume plan

1. Review and decide open decision 1; get this draft PR reviewed.
2. Submit the targeted patch: `evergreen patch -p mongodb-kubernetes -v e2e_mdb_kind_ubi_cloudqa -v e2e_static_mdb_kind_ubi_cloudqa -v e2e_custom_domain_mdb_kind_ubi_cloudqa -t e2e_replica_set -d "<branch>: [AI] optimistic status write pilot" -f -y`, then `evg set_priority <id> --priority 40`. Check the operator logs for conflict frequency and for self-conflicts (a conflict at the annotation write on a current object means a missed version refresh).
3. Add unit tests for the stale path of every other write to the MongoDB CR in the replica set controller (an audit of other CR writes in that controller found none besides `SetAnnotations`, reported by the implementer, not independently confirmed).
4. Extend per kind (standalone, sharded, MRS, OM, user, search, VoyageAI). Prerequisites per kind: move pre-`UpdateStatus` status edits into options (MRS `FeatureCompatibilityVersion`, Search `MetricsForwarder`/`LoadBalancer` nils), audit every CR write in that controller for the version chain, handle the sharded `sc.Status = *deploymentState.Status` restore, and handle the sub-path writes of OM and Search (a PUT sends the whole status; safe under the version check but a behaviour change to review). Search needs the most care (three writers).
5. Fix the Search `MetricsForwarder = nil` then `UpdateStatus(OK)` bug if confirmed at runtime (it recreates `Running` instead of clearing).
6. Revisit the remaining flaky tasks: tls certs prefix rotation timeouts, OpenShift scram timeouts and CPU capacity, the x509 `test_create_database_resource` timeout, and the unexplained 610s sharded create failure.

## 12. Tooling notes

- Use `GOTOOLCHAIN=go1.26.9` for Go; the default 1.27.1 overflows the stack in existing controller tests.
- `evergreen` CLI needs a device login (`evergreen login`); `patch` hangs until approved. `evg wait_for_patch` is broken against the current Evergreen schema (`taskCount` no longer exists); poll the version API instead. The devprod MCP evg tools are rate limited (about an hour window); the `evg` CLI is the fallback.
- The `ty` pre-commit hook may fail with many diagnostics because it cannot resolve imports from the tests venv; it failed identically on clean `origin/master` when checked once.
- `scripts/release/check_changelog.py` needs Python dependencies that were not installed locally; changelog files follow the format of existing ones.
- User preferences: brief comments and changelogs; never push or open a PR without explicit go-ahead; prefer root cause over blanket retries; target Evergreen patches at affected tasks with priority 40.
