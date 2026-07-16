# AppDBReconciler Interface Split Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Enforce that `opsManager.Spec.AppDB` is only ever read from within the internal `ReconcileAppDbReplicaSet` reconciler by introducing a shared `AppDBReconciler` interface with two implementations — the existing internal reconciler and a new `ExternalAppDBReconciler` that never touches `.Spec.AppDB` — and routing `MongoDBOpsManager.Reconcile()` through the interface instead of branching on `ExternalApplicationDatabaseRef` at each call site.

**Architecture:** `Reconcile()` picks exactly one `AppDBReconciler` implementation per reconcile (internal vs. external) based on `opsManager.Spec.ExternalApplicationDatabaseRef`, then calls `ReconcileAppDB` and `GetConnectionString` on it unconditionally through the interface. Internal-AppDB-only migration steps (container-count architecture change, re-adoption, StatefulSet watch registration) stay as explicit steps guarded in the `else` branch of the reconciler-selection block, since they have no external-mode equivalent and aren't part of the shared interface. Peripheral packages (`watch/predicates.go`, `vaultwatcher`, `telemetry/collector.go`) get direct nil-guards on `ExternalApplicationDatabaseRef` since they're not reconciler code and don't participate in the interface.

**Tech Stack:** Go, controller-runtime, existing `workflow.Status`/`mdbstatus` conventions already used throughout `controllers/operator`.

---

### Task 1: Add the `AppDBReconciler` interface

**Files:**
- Create: `controllers/operator/appdb_reconciler.go`

- [ ] **Step 1: Write the interface file**

```go
package operator

import (
	"context"

	"go.uber.org/zap"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	omv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/om"
)

// AppDBReconciler is implemented by both the internal AppDB reconciler
// (*ReconcileAppDbReplicaSet, backed by opsManager.Spec.AppDB) and the
// ExternalAppDBReconciler (backed by opsManager.Spec.ExternalApplicationDatabaseRef).
// MongoDBOpsManager.Reconcile selects exactly one implementation per reconcile and
// drives it through this interface, so callers never need to branch on which AppDB
// mode is active.
type AppDBReconciler interface {
	// ReconcileAppDB brings the AppDB (internal StatefulSet, or external CR reference)
	// to the desired state. Returns the same (reconcile.Result, error) contract as the
	// rest of the controller's workflow.Status.ReconcileResult() calls.
	ReconcileAppDB(ctx context.Context, opsManager *omv1.MongoDBOpsManager) (reconcile.Result, error)

	// GetConnectionString returns the MongoDB connection string OpsManager/BackupDaemon
	// should use to reach the AppDB. It is a pure computation — callers are responsible
	// for writing the result into each member cluster's connection-string secret.
	GetConnectionString(ctx context.Context, opsManager *omv1.MongoDBOpsManager, log *zap.SugaredLogger) (string, error)
}
```

- [ ] **Step 2: Verify it compiles**

Run: `cd /Users/maciej.karas/mongodb/mongodb-kubernetes && go build -mod=mod ./controllers/...`
Expected: no errors (interface has no implementations wired up yet, so nothing uses it — this just checks syntax/imports).

- [ ] **Step 3: Commit**

```bash
git add controllers/operator/appdb_reconciler.go
git commit -m "feat: add AppDBReconciler interface for internal/external AppDB split"
```

---

### Task 2: Add `GetConnectionString` to the internal reconciler

**Files:**
- Modify: `controllers/operator/appdbreplicaset_controller.go` (near `getCurrentStatefulsetHostnames` at line 2145, and `ensureAppDbPassword` at line 1595)
- Test: `controllers/operator/appdbreplicaset_controller_test.go`

The existing internal-mode connection-string logic (today inlined at `mongodbopsmanager_controller.go:502-507`) is:
```go
appDBPassword, err := appDbReconciler.ensureAppDbPassword(ctx, opsManager, log)
if err != nil {
	return r.updateStatus(ctx, opsManager, workflow.Failed(xerrors.Errorf("Error getting AppDB password: %w", err)), log, opsManagerExtraStatusParams)
}
appDBConnectionString = buildMongoConnectionUrl(opsManager, appDBPassword, appDbReconciler.getCurrentStatefulsetHostnames(opsManager))
```

This task extracts the password+hostname+URL-build sequence into a `GetConnectionString` method so `*ReconcileAppDbReplicaSet` satisfies `AppDBReconciler`. The member-cluster secret-write loop (currently the `for _, memberCluster := range ...` block right after this snippet) stays in `Reconcile()` — see Task 5 — since it's identical logic regardless of which reconciler produced the string, and unifying it there avoids giving the internal reconciler a back-reference to `*OpsManagerReconciler`.

- [ ] **Step 1: Add the method**

Insert directly after `getCurrentStatefulsetHostnames` (after line 2149) in `controllers/operator/appdbreplicaset_controller.go`:

```go
// GetConnectionString implements AppDBReconciler. It computes the connection string from the
// internal AppDB's password secret and current StatefulSet hostnames — the same sequence
// Reconcile() used to inline directly.
func (r *ReconcileAppDbReplicaSet) GetConnectionString(ctx context.Context, opsManager *omv1.MongoDBOpsManager, log *zap.SugaredLogger) (string, error) {
	appDBPassword, err := r.ensureAppDbPassword(ctx, opsManager, log)
	if err != nil {
		return "", xerrors.Errorf("Error getting AppDB password: %w", err)
	}

	return buildMongoConnectionUrl(opsManager, appDBPassword, r.getCurrentStatefulsetHostnames(opsManager)), nil
}
```

Check the top of `appdbreplicaset_controller.go` already imports `xerrors` (`github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/...` or whichever xerrors package `mongodbopsmanager_controller.go` uses — confirm via `grep -n '"golang.org/x/xerrors"\|xerrors"' controllers/operator/appdbreplicaset_controller.go` before adding the method; it is already imported there since other methods in the file use `xerrors.Errorf`, e.g. `shouldReconcileAppDB`).

- [ ] **Step 2: Write a unit test**

Add to `controllers/operator/appdbreplicaset_controller_test.go` (follow the existing pattern in that file for constructing a reconciler via `defaultTestOmReconciler`/`NewAppDBReplicaSetReconciler` and a built `MongoDBOpsManager` — mirror how `TestComputeExternalAppDBConnectionString_WritesFixedSecret` in `mongodbopsmanager_controller_test.go` sets up its OM/reconciler, but using the internal (non-external-ref) `DefaultOpsManagerBuilder().Build()`):

```go
func TestReconcileAppDbReplicaSet_GetConnectionString(t *testing.T) {
	ctx := context.Background()
	testOm := DefaultOpsManagerBuilder().Build()

	reconciler, _, _ := defaultTestOmReconciler(ctx, t, nil, "", "", testOm, nil, om.NewDefaultCachedOMConnectionFactory(), architectures.NonStatic)
	appDbReconciler, err := reconciler.createNewAppDBReconciler(ctx, testOm, zap.S())
	require.NoError(t, err)

	connString, err := appDbReconciler.GetConnectionString(ctx, testOm, zap.S())
	require.NoError(t, err)
	assert.Contains(t, connString, util.OpsManagerMongoDBUserName)
}
```

Adjust imports/helper names to match whatever is already imported at the top of `appdbreplicaset_controller_test.go` — if `defaultTestOmReconciler` or `DefaultOpsManagerBuilder` live in a different test file in the same package (`controllers/operator`), no import changes are needed since Go test files in one package share scope.

- [ ] **Step 3: Run the test**

Run: `cd /Users/maciej.karas/mongodb/mongodb-kubernetes && go test -mod=mod ./controllers/operator/... -run TestReconcileAppDbReplicaSet_GetConnectionString -v`
Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add controllers/operator/appdbreplicaset_controller.go controllers/operator/appdbreplicaset_controller_test.go
git commit -m "feat: add GetConnectionString to internal AppDB reconciler"
```

---

### Task 3: Create `ExternalAppDBReconciler`

**Files:**
- Create: `controllers/operator/external_appdb_reconciler.go`
- Modify: `controllers/operator/mongodbopsmanager_controller.go` — remove the member-cluster write loop from `computeExternalAppDBConnectionString` (lines 988-992), drop its now-unused `memberClusters` parameter, and update its call site at `TestComputeExternalAppDBConnectionString_WritesFixedSecret`
- Test: `controllers/operator/mongodbopsmanager_controller_test.go`

`computeExternalAppDBConnectionString` currently both computes the string *and* writes it into every member cluster's secret (lines 988-992 in `mongodbopsmanager_controller.go`):

```go
	for _, memberCluster := range memberClusters {
		if err := r.ensureAppDBConnectionStringInMemberCluster(ctx, opsManager, connectionString, memberCluster, log); err != nil {
			return "", xerrors.Errorf("error ensuring AppDB connection string in cluster %s: %w", memberCluster.Name, err)
		}
	}

	return connectionString, nil
}
```

To make `GetConnectionString` a pure computation on both interface implementations (matching Task 2's internal version, which has no side effects), this write loop moves to `Reconcile()` itself in Task 5, shared by both modes.

- [ ] **Step 1: Remove the write loop from `computeExternalAppDBConnectionString`**

In `controllers/operator/mongodbopsmanager_controller.go`, change the function signature (line 955) from:

```go
func (r *OpsManagerReconciler) computeExternalAppDBConnectionString(ctx context.Context, opsManager *omv1.MongoDBOpsManager, memberClusters []multicluster.MemberCluster, log *zap.SugaredLogger) (string, error) {
```

to:

```go
func (r *OpsManagerReconciler) computeExternalAppDBConnectionString(ctx context.Context, opsManager *omv1.MongoDBOpsManager) (string, error) {
```

And delete the `for _, memberCluster := range memberClusters { ... }` block (lines 988-992), leaving the function ending with:

```go
	default:
		return "", xerrors.Errorf("externalApplicationDatabaseRef.kind %q is not supported", ref.Kind)
	}

	return connectionString, nil
}
```

(The `log *zap.SugaredLogger` parameter becomes unused in this function after removing the loop — remove it from the signature too, since nothing else in the function uses `log`. Final signature: `func (r *OpsManagerReconciler) computeExternalAppDBConnectionString(ctx context.Context, opsManager *omv1.MongoDBOpsManager) (string, error)`.)

- [ ] **Step 2: Write the `ExternalAppDBReconciler` type**

```go
package operator

import (
	"context"

	"go.uber.org/zap"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	omv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/om"
	mdbstatus "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/status"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/workflow"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/xerrors"
)

// ExternalAppDBReconciler implements AppDBReconciler for OpsManager resources using
// spec.externalApplicationDatabaseRef. It never reads opsManager.Spec.AppDB — all AppDB
// state comes from the referenced MongoDB/MongoDBMultiCluster CR instead.
type ExternalAppDBReconciler struct {
	reconciler *OpsManagerReconciler
	log        *zap.SugaredLogger
}

func (r *OpsManagerReconciler) createExternalAppDBReconciler(log *zap.SugaredLogger) *ExternalAppDBReconciler {
	return &ExternalAppDBReconciler{reconciler: r, log: log}
}

// ReconcileAppDB validates the externalApplicationDatabaseRef, performs the one-time
// detach-and-adopt migration of any pre-existing internal AppDB (idempotent, no-op once
// complete), and establishes a watch on the referenced CR.
func (e *ExternalAppDBReconciler) ReconcileAppDB(ctx context.Context, opsManager *omv1.MongoDBOpsManager) (reconcile.Result, error) {
	appDbStatusOption := mdbstatus.NewOMPartOption(mdbstatus.AppDb)

	if err := e.reconciler.validateExternalAppDBReference(ctx, opsManager); err != nil {
		return e.reconciler.updateStatus(ctx, opsManager, workflow.Failed(xerrors.Errorf("Error validating externalApplicationDatabaseRef: %w", err)), e.log, appDbStatusOption)
	}

	if err := e.reconciler.detachInternalAppDB(ctx, opsManager, e.log); err != nil {
		return e.reconciler.updateStatus(ctx, opsManager, workflow.Failed(xerrors.Errorf("Error detaching internal AppDB: %w", err)), e.log, appDbStatusOption)
	}

	e.reconciler.watchExternalAppDBReference(opsManager)

	return e.reconciler.updateStatus(ctx, opsManager, workflow.OK(), e.log, appDbStatusOption)
}

// GetConnectionString computes the AppDB connection string from the referenced
// MongoDB/MongoDBMultiCluster CR. It is a pure computation — writing the result into each
// member cluster's connection-string secret is the caller's responsibility (Reconcile).
func (e *ExternalAppDBReconciler) GetConnectionString(ctx context.Context, opsManager *omv1.MongoDBOpsManager, log *zap.SugaredLogger) (string, error) {
	return e.reconciler.computeExternalAppDBConnectionString(ctx, opsManager)
}
```

Before writing this file, confirm the exact import path for `xerrors` and `workflow` used in `mongodbopsmanager_controller.go` (`grep -n '"github.com/mongodb/mongodb-kubernetes/controllers/operator/workflow"\|xerrors"' controllers/operator/mongodbopsmanager_controller.go`) and match them exactly — do not guess the module path.

- [ ] **Step 3: Update the now-broken test call site**

In `controllers/operator/mongodbopsmanager_controller_test.go`, `TestComputeExternalAppDBConnectionString_WritesFixedSecret` (around line 1929) currently does:

```go
	helper, err := NewOpsManagerReconcilerHelper(ctx, reconciler, testOm, reconciler.memberClustersMap, zap.S())
	require.NoError(t, err)

	connString, err := reconciler.computeExternalAppDBConnectionString(ctx, testOm, helper.getHealthyMemberClusters(), zap.S())
	require.NoError(t, err)
	assert.Contains(t, connString, util.OpsManagerMongoDBUserName)
	assert.Contains(t, connString, "test-password")

	result := corev1.Secret{}
	require.NoError(t, kubeClient.Get(ctx, kube.ObjectKey(testOm.Namespace, testOm.AppDBMongoConnectionStringSecretName()), &result))
	assert.Contains(t, string(result.Data[util.AppDbConnectionStringKey]), util.OpsManagerMongoDBUserName)
```

Since the write side-effect moved out of `computeExternalAppDBConnectionString`, replace it with a call to the now-pure function plus an explicit write loop (mirroring what `Reconcile()` itself will do in Task 5), so the test still exercises and asserts the secret-write behavior:

```go
	helper, err := NewOpsManagerReconcilerHelper(ctx, reconciler, testOm, reconciler.memberClustersMap, zap.S())
	require.NoError(t, err)

	connString, err := reconciler.computeExternalAppDBConnectionString(ctx, testOm)
	require.NoError(t, err)
	assert.Contains(t, connString, util.OpsManagerMongoDBUserName)
	assert.Contains(t, connString, "test-password")

	for _, memberCluster := range helper.getHealthyMemberClusters() {
		require.NoError(t, reconciler.ensureAppDBConnectionStringInMemberCluster(ctx, testOm, connString, memberCluster, zap.S()))
	}

	result := corev1.Secret{}
	require.NoError(t, kubeClient.Get(ctx, kube.ObjectKey(testOm.Namespace, testOm.AppDBMongoConnectionStringSecretName()), &result))
	assert.Contains(t, string(result.Data[util.AppDbConnectionStringKey]), util.OpsManagerMongoDBUserName)
```

- [ ] **Step 4: Build and run the affected tests**

Run: `cd /Users/maciej.karas/mongodb/mongodb-kubernetes && go build -mod=mod ./controllers/... && go test -mod=mod ./controllers/operator/... -run 'TestComputeExternalAppDBConnectionString_WritesFixedSecret' -v`
Expected: builds clean, test PASSes.

- [ ] **Step 5: Commit**

```bash
git add controllers/operator/external_appdb_reconciler.go controllers/operator/mongodbopsmanager_controller.go controllers/operator/mongodbopsmanager_controller_test.go
git commit -m "feat: add ExternalAppDBReconciler, make computeExternalAppDBConnectionString pure"
```

---

### Task 4: Move `ProcessValidationsOnReconcile` earlier and hoist the always-run watch registrations

**Files:**
- Modify: `controllers/operator/mongodbopsmanager_controller.go` (`Reconcile`, lines ~403-486)

This task performs the reordering inside `Reconcile()` in isolation before Task 5 swaps in the interface, so each change stays reviewable on its own.

Current relevant order (lines 407-486, abbreviated to the parts that move):

```go
	// ... version check (416) ...

	//TODO this should be first step of the ExternalAppDBReconciler or helper
	if err := r.validateExternalAppDBReference(ctx, opsManager); err != nil { ... }

	if opsManager.Spec.ExternalApplicationDatabaseRef != nil {
		if err := r.detachInternalAppDB(ctx, opsManager, log); err != nil { ... }
	}

	//TODO create this reconciler conditionally, only when ExternalApplicationDatabaseRef is not set
	appDbReconciler, err := r.createNewAppDBReconciler(ctx, opsManager, log)
	if err != nil { ... }

	//TODO run this earlier
	if part, err := opsManager.ProcessValidationsOnReconcile(); err != nil { ... }

	//TODO why this cannot be part of regular appDbReconciler
	if opsManager.Spec.ExternalApplicationDatabaseRef == nil {
		acClient := appDbReconciler.getMemberCluster(appDbReconciler.getNameOfFirstMemberCluster()).Client
		if err := ensureResourcesForArchitectureChange(ctx, acClient, r.SecretClient, opsManager); err != nil { ... }
	}

	if err := ensureSharedGlobalResources(ctx, r.client, opsManager); err != nil { ... }

	// 1. Reconcile AppDB
	emptyResult, _ := workflow.OK().ReconcileResult()
	retryResult := reconcile.Result{Requeue: true}

	appDBReplicaSet := opsManager.Spec.AppDB
	var result reconcile.Result
	//TODO should be part of regular appDbReconciler
	if opsManager.Spec.ExternalApplicationDatabaseRef == nil {
		adopted, err := r.reAdoptInternalAppDBIfNeeded(ctx, opsManager)
		if err != nil { ... }
		if !adopted { ... }

		// TODO: here SetupCommonWatchers might also run logic for the OpsManager resources, so not sure what should we do here
		// TODO: make SetupCommonWatchers support opsmanager watcher setup
		r.SetupCommonWatchers(appDBReplicaSet, nil, nil, appDBReplicaSet.GetName())

		//TODO this and the next watcher for backup should be run also for the externalAppDB and not omitted.
		if opsManager.IsTLSEnabled() {
			r.resourceWatcher.RegisterWatchedTLSResources(opsManager.ObjectKey(), opsManager.Spec.GetOpsManagerCA(), []string{opsManager.TLSCertificateSecretName()})
		}
		r.watchMongoDBResourcesReferencedByBackup(ctx, opsManager, log)

		result, err = appDbReconciler.ReconcileAppDB(ctx, opsManager)
		if err != nil || (result != emptyResult && result != retryResult) {
			return result, err
		}
	}
```

- [ ] **Step 1: Replace this whole section**

Replace it with:

```go
	// ... version check (416) stays exactly where it is ...

	if part, err := opsManager.ProcessValidationsOnReconcile(); err != nil {
		return r.updateStatus(ctx, opsManager, workflow.Invalid("%s", err.Error()), log, mdbstatus.NewOMPartOption(part))
	}

	if err := ensureSharedGlobalResources(ctx, r.client, opsManager); err != nil {
		return r.updateStatus(ctx, opsManager, workflow.Failed(xerrors.Errorf("Error ensuring shared global resources %w", err)), log, opsManagerExtraStatusParams)
	}

	// These watches cover OpsManager's own TLS certificate and backup-referenced MongoDB
	// resources — neither is AppDB-mode-specific, so they run for both internal and
	// external AppDB.
	if opsManager.IsTLSEnabled() {
		r.resourceWatcher.RegisterWatchedTLSResources(opsManager.ObjectKey(), opsManager.Spec.GetOpsManagerCA(), []string{opsManager.TLSCertificateSecretName()})
	}
	r.watchMongoDBResourcesReferencedByBackup(ctx, opsManager, log)

	// 1. Reconcile AppDB
	emptyResult, _ := workflow.OK().ReconcileResult()
	retryResult := reconcile.Result{Requeue: true}

	var appDbReconciler AppDBReconciler
	if opsManager.Spec.ExternalApplicationDatabaseRef != nil {
		appDbReconciler = r.createExternalAppDBReconciler(log)
	} else {
		internalAppDbReconciler, err := r.createNewAppDBReconciler(ctx, opsManager, log)
		if err != nil {
			return r.updateStatus(ctx, opsManager, workflow.Failed(xerrors.Errorf("Error initializing AppDB reconciler: %w", err)), log, opsManagerExtraStatusParams)
		}

		acClient := internalAppDbReconciler.getMemberCluster(internalAppDbReconciler.getNameOfFirstMemberCluster()).Client
		if err := ensureResourcesForArchitectureChange(ctx, acClient, r.SecretClient, opsManager); err != nil {
			return r.updateStatus(ctx, opsManager, workflow.Failed(xerrors.Errorf("Error ensuring resources for upgrade from 1 to 3 container AppDB: %w", err)), log, opsManagerExtraStatusParams)
		}

		adopted, err := r.reAdoptInternalAppDBIfNeeded(ctx, opsManager)
		if err != nil {
			return r.updateStatus(ctx, opsManager, workflow.Failed(err), log, opsManagerExtraStatusParams)
		}
		if !adopted {
			return r.updateStatus(ctx, opsManager, workflow.Pending("waiting for MongoDB controller to finish detaching AppDB StatefulSet"), log, opsManagerExtraStatusParams)
		}

		// TODO: make SetupCommonWatchers support opsmanager watcher setup
		// The order matters here, since appDB and opsManager share the same reconcile ObjectKey being opsmanager crd
		// That means we need to remove first, which SetupCommonWatchers does, then register additional watches
		r.SetupCommonWatchers(opsManager.Spec.AppDB, nil, nil, opsManager.Spec.AppDB.GetName())

		appDbReconciler = internalAppDbReconciler
	}

	result, err := appDbReconciler.ReconcileAppDB(ctx, opsManager)
	if err != nil || (result != emptyResult && result != retryResult) {
		return result, err
	}
```

Notes on this rewrite:
- `appDBReplicaSet := opsManager.Spec.AppDB` is gone as a top-level variable; the two former uses (`SetupCommonWatchers` and the vault-path line later in the function) now read `opsManager.Spec.AppDB` directly only inside the internal (`else`) branch, or — for the vault-path line at the old line 544 (`appDBReplicaSet.Namespace`) — simply use `opsManager.Namespace` instead (see Task 6, which handles the vault block; `AppDBSpec.Namespace` is always defaulted to `opsManager.Namespace` by `InitDefaultFields()`, so this is a value-preserving simplification, not a behavior change).
- The three `//TODO ... should be part of regular appDbReconciler` comments are resolved by this restructuring: those steps are internal-AppDB-specific migration concerns (container-count architecture change, re-adoption, StatefulSet watch) with no external-mode equivalent, so they stay as explicit steps in the `else` branch rather than being pulled into the shared interface.
- The `//TODO this and the next watcher ... should be run also for the externalAppDB` comment is resolved by hoisting those two calls above the branch entirely.
- `err` was previously declared once near the top (`semverVersion, err := ...`) and reused with `=` at several points; introducing `result, err := appDbReconciler.ReconcileAppDB(...)` as a fresh `:=` is correct here since this is the first use of an `err` at this scope inside the new block — check the surrounding code compiles (Go will flag if `err` was already declared in the same block scope; if so, change `:=` to `=` for `result, err`).

- [ ] **Step 2: Build**

Run: `cd /Users/maciej.karas/mongodb/mongodb-kubernetes && go build -mod=mod ./controllers/...`
Expected: compiles. Fix any `err`/`result` redeclaration issues per the note above, and confirm `mdbstatus` and `xerrors` are already imported (they are, per earlier grep).

- [ ] **Step 3: Commit**

```bash
git add controllers/operator/mongodbopsmanager_controller.go
git commit -m "refactor: route Reconcile through AppDBReconciler interface, hoist shared watch registration"
```

---

### Task 5: Replace the connection-string if/else with the unified interface call

**Files:**
- Modify: `controllers/operator/mongodbopsmanager_controller.go` (the block right after Task 4's edit, originally lines 488-513)

Current code (unchanged by Task 4, still present after it):

```go
	opsManagerReconcilerHelper, err := NewOpsManagerReconcilerHelper(ctx, r, opsManager, r.memberClustersMap, log)
	if err != nil {
		return r.updateStatus(ctx, opsManager, workflow.Failed(err), log, opsManagerExtraStatusParams)
	}

	var appDBConnectionString string
	if opsManager.Spec.ExternalApplicationDatabaseRef != nil {
		r.watchExternalAppDBReference(opsManager)
		connString, err := r.computeExternalAppDBConnectionString(ctx, opsManager, opsManagerReconcilerHelper.getHealthyMemberClusters(), log)
		if err != nil {
			return r.updateStatus(ctx, opsManager, workflow.Failed(xerrors.Errorf("Error computing external AppDB connection string: %w", err)), log, opsManagerExtraStatusParams)
		}
		appDBConnectionString = connString
	} else {
		appDBPassword, err := appDbReconciler.ensureAppDbPassword(ctx, opsManager, log)
		if err != nil {
			return r.updateStatus(ctx, opsManager, workflow.Failed(xerrors.Errorf("Error getting AppDB password: %w", err)), log, opsManagerExtraStatusParams)
		}

		appDBConnectionString = buildMongoConnectionUrl(opsManager, appDBPassword, appDbReconciler.getCurrentStatefulsetHostnames(opsManager))
		for _, memberCluster := range opsManagerReconcilerHelper.getHealthyMemberClusters() {
			if err := r.ensureAppDBConnectionStringInMemberCluster(ctx, opsManager, appDBConnectionString, memberCluster, log); err != nil {
				return r.updateStatus(ctx, opsManager, workflow.Failed(xerrors.Errorf("error ensuring AppDB connection string in cluster %s: %w", memberCluster.Name, err)), log, opsManagerExtraStatusParams)
			}
		}
	}
```

Note: `r.watchExternalAppDBReference(opsManager)` here is now redundant with Task 3's `ExternalAppDBReconciler.ReconcileAppDB`, which already calls it — remove the duplicate call as part of this edit.

- [ ] **Step 1: Replace with the unified call**

```go
	opsManagerReconcilerHelper, err := NewOpsManagerReconcilerHelper(ctx, r, opsManager, r.memberClustersMap, log)
	if err != nil {
		return r.updateStatus(ctx, opsManager, workflow.Failed(err), log, opsManagerExtraStatusParams)
	}

	appDBConnectionString, err := appDbReconciler.GetConnectionString(ctx, opsManager, log)
	if err != nil {
		return r.updateStatus(ctx, opsManager, workflow.Failed(xerrors.Errorf("Error computing AppDB connection string: %w", err)), log, opsManagerExtraStatusParams)
	}

	for _, memberCluster := range opsManagerReconcilerHelper.getHealthyMemberClusters() {
		if err := r.ensureAppDBConnectionStringInMemberCluster(ctx, opsManager, appDBConnectionString, memberCluster, log); err != nil {
			return r.updateStatus(ctx, opsManager, workflow.Failed(xerrors.Errorf("error ensuring AppDB connection string in cluster %s: %w", memberCluster.Name, err)), log, opsManagerExtraStatusParams)
		}
	}
```

- [ ] **Step 2: Build and run the OM controller test suite**

Run: `cd /Users/maciej.karas/mongodb/mongodb-kubernetes && go build -mod=mod ./controllers/... && go test -mod=mod ./controllers/operator/... -run 'TestReconcile|TestComputeExternalAppDBConnectionString|TestOpsManager' -v`
Expected: builds clean; watch specifically for `TestReconcile_ExternalAppDBRef_NeverCreatesInternalPasswordSecret` and any other external-AppDB-ref reconcile tests — they must still pass since `appDBConnectionString` computation and secret-writing behavior is unchanged, just relocated.

- [ ] **Step 3: Commit**

```bash
git add controllers/operator/mongodbopsmanager_controller.go
git commit -m "refactor: unify AppDB connection-string computation through AppDBReconciler.GetConnectionString"
```

---

### Task 6: Fix the remaining unconditional `.Spec.AppDB` reads (vault block, predicates, vaultwatcher, telemetry)

**Files:**
- Modify: `controllers/operator/mongodbopsmanager_controller.go` (vault annotation block, ~line 541 after Task 4/5's edits — verify exact line with `grep -n 'vault.IsVaultSecretBackend' controllers/operator/mongodbopsmanager_controller.go`)
- Modify: `controllers/operator/watch/predicates.go` (line 67)
- Modify: `pkg/vault/vaultwatcher/vaultsecretwatch.go` (line 78)
- Modify: `pkg/telemetry/collector.go` (line 314)
- Test: `controllers/operator/watch/predicates_test.go`, and any existing telemetry/vaultwatcher tests if present (check with `grep -rln 'vaultsecretwatch\|collector_test' pkg/vault/vaultwatcher pkg/telemetry`)

This task fixes the peripheral-package TODOs confirmed in scope (not the deferred TLS/CA ones).

- [ ] **Step 1: Fix `mongodbopsmanager_controller.go`'s vault block**

The vault block (already partially fixed in a prior commit — confirm current state matches):

```go
	if vault.IsVaultSecretBackend() {
		vaultMap := make(map[string]string)
		for _, s := range opsManager.GetSecretsMountedIntoPod() {
			path := fmt.Sprintf("%s/%s/%s", r.VaultClient.OpsManagerSecretMetadataPath(), appDBReplicaSet.Namespace, s)
			vaultMap = merge.StringToStringMap(vaultMap, r.VaultClient.GetSecretAnnotation(path))
		}
		if opsManager.Spec.ExternalApplicationDatabaseRef == nil {
			for _, s := range opsManager.Spec.AppDB.GetSecretsMountedIntoPod() {
				path := fmt.Sprintf("%s/%s/%s", r.VaultClient.AppDBSecretMetadataPath(), appDBReplicaSet.Namespace, s)
				vaultMap = merge.StringToStringMap(vaultMap, r.VaultClient.GetSecretAnnotation(path))
			}
		}
		...
```

Since Task 4 removed the top-level `appDBReplicaSet` variable, replace both remaining `appDBReplicaSet.Namespace` references in this block with `opsManager.Namespace` (equivalent value, see Task 4's note):

```go
	if vault.IsVaultSecretBackend() {
		vaultMap := make(map[string]string)
		for _, s := range opsManager.GetSecretsMountedIntoPod() {
			path := fmt.Sprintf("%s/%s/%s", r.VaultClient.OpsManagerSecretMetadataPath(), opsManager.Namespace, s)
			vaultMap = merge.StringToStringMap(vaultMap, r.VaultClient.GetSecretAnnotation(path))
		}
		if opsManager.Spec.ExternalApplicationDatabaseRef == nil {
			for _, s := range opsManager.Spec.AppDB.GetSecretsMountedIntoPod() {
				path := fmt.Sprintf("%s/%s/%s", r.VaultClient.AppDBSecretMetadataPath(), opsManager.Namespace, s)
				vaultMap = merge.StringToStringMap(vaultMap, r.VaultClient.GetSecretAnnotation(path))
			}
		}
		...
```

(This block is already correctly gated on `ExternalApplicationDatabaseRef == nil` from earlier work — this step is purely fixing the now-dangling `appDBReplicaSet` variable reference left by Task 4, not changing gating logic.)

- [ ] **Step 2: Fix `watch/predicates.go`**

Current (line ~55-73):

```go
func PredicatesForOpsManager() predicate.Funcs {
	return predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			...
				for _, e := range oldResource.GetSecretsMountedIntoPod() {
					if oldResource.GetAnnotations()[e] != newResource.GetAnnotations()[e] {
						return true
					}
				}

				//TODO: exclude when externalAppDB
				for _, e := range oldResource.Spec.AppDB.GetSecretsMountedIntoPod() {
					if oldResource.GetAnnotations()[e] != newResource.GetAnnotations()[e] {
						return true
					}
				}
			...
```

Change to:

```go
				if oldResource.Spec.ExternalApplicationDatabaseRef == nil {
					for _, e := range oldResource.Spec.AppDB.GetSecretsMountedIntoPod() {
						if oldResource.GetAnnotations()[e] != newResource.GetAnnotations()[e] {
							return true
						}
					}
				}
```

Read the full function first (`controllers/operator/watch/predicates.go:42-75` per the earlier grep) to get the exact surrounding braces/variable names (`oldResource`, `newResource`) right before editing — variable names above are inferred from the grep context and must be confirmed against the actual file.

- [ ] **Step 3: Fix `pkg/vault/vaultwatcher/vaultsecretwatch.go`**

Current (~line 70-83):

```go
			//TODO: what to do when ExternalAppDB
			for _, secretName := range om.Spec.AppDB.GetSecretsMountedIntoPod() {
				path := fmt.Sprintf("%s/%s/%s", vaultClient.AppDBSecretMetadataPath(), om.Namespace, secretName)
				latestResourceVersion, currentResourceVersion := getCurrentAndLatestVersion(vaultClient, path, secretName, om.Annotations, log)

				if latestResourceVersion > currentResourceVersion {
					watchChannel <- event.GenericEvent{Object: &omList.Items[n]}
					break
				}
			}
```

Change to:

```go
			if om.Spec.ExternalApplicationDatabaseRef == nil {
				for _, secretName := range om.Spec.AppDB.GetSecretsMountedIntoPod() {
					path := fmt.Sprintf("%s/%s/%s", vaultClient.AppDBSecretMetadataPath(), om.Namespace, secretName)
					latestResourceVersion, currentResourceVersion := getCurrentAndLatestVersion(vaultClient, path, secretName, om.Annotations, log)

					if latestResourceVersion > currentResourceVersion {
						watchChannel <- event.GenericEvent{Object: &omList.Items[n]}
						break
					}
				}
			}
```

- [ ] **Step 4: Fix `pkg/telemetry/collector.go`**

Current (~line 312-315):

```go
			omClusters := len(item.Spec.ClusterSpecList)
			//TODO if externalAppDB, we can read it from the CR or just ignore
			appDBClusters := len(item.Spec.AppDB.ClusterSpecList)
```

Change to:

```go
			omClusters := len(item.Spec.ClusterSpecList)
			var appDBClusters int
			if item.Spec.ExternalApplicationDatabaseRef == nil {
				appDBClusters = len(item.Spec.AppDB.ClusterSpecList)
			}
```

- [ ] **Step 5: Run the affected test suites**

Run: `cd /Users/maciej.karas/mongodb/mongodb-kubernetes && go build -mod=mod ./... && go test -mod=mod ./controllers/operator/watch/... ./pkg/vault/... ./pkg/telemetry/... -v`
Expected: all PASS. Pay particular attention to `controllers/operator/watch/predicates_test.go`'s `TestPredicatesForOpsManager` — confirm it doesn't need a new external-ref test case added; if the existing table only covers internal-AppDB OM resources, add one case with `ExternalApplicationDatabaseRef` set to confirm the predicate no longer panics/misbehaves on a nil-safe `AppDB.GetSecretsMountedIntoPod()` (recall `AppDB` is always non-nil due to `InitDefaultFields()`, so this guards intent/correctness, not a nil-pointer risk).

- [ ] **Step 6: Commit**

```bash
git add controllers/operator/mongodbopsmanager_controller.go controllers/operator/watch/predicates.go pkg/vault/vaultwatcher/vaultsecretwatch.go pkg/telemetry/collector.go controllers/operator/watch/predicates_test.go
git commit -m "fix: guard peripheral AppDB reads (vault watch, predicates, telemetry) on ExternalApplicationDatabaseRef"
```

---

### Task 7: Leave deferred TLS/CA TODOs marked, full regression pass

**Files:**
- Modify: `controllers/operator/construct/opsmanager_construction.go` (comment only)
- Modify: `controllers/operator/mongodbopsmanager_controller.go` (comment only, `ensureConfiguration`)

- [ ] **Step 1: Replace the ad-hoc TODO comments with a reference to the deferred follow-up**

In `controllers/operator/construct/opsmanager_construction.go`, change:

```go
		//TODO: this CAConfig is problematic and not sure where get it from
		AppDBTlsCAConfigMapName: opsManager.Spec.AppDB.GetCAConfigMapName(),
```

to:

```go
		// TODO(CLOUDP-TBD): AppDBTlsCAConfigMapName is computed from the internal AppDB spec
		// even in external-AppDB mode, so OM/BackupDaemon won't trust the external CR's actual
		// CA. Tracked as a separate PR (TLS/CA parity for externalApplicationDatabaseRef) — not
		// fixed here.
		AppDBTlsCAConfigMapName: opsManager.Spec.AppDB.GetCAConfigMapName(),
```

Do the equivalent for the two `ensureConfiguration` call sites in `controllers/operator/mongodbopsmanager_controller.go` (`Security.IsTLSEnabled()` and `GetCAConfigMapName()`, originally reported at ~lines 1107/1110 — re-locate with `grep -n 'Spec.AppDB.Security.IsTLSEnabled\|Spec.AppDB.GetCAConfigMapName' controllers/operator/mongodbopsmanager_controller.go` since line numbers will have shifted after Tasks 4-6), adding the same one-line pointer to the deferred PR rather than a bare "not sure" TODO. If you (the implementer) have an actual Jira ticket key for this follow-up, use it in place of `CLOUDP-TBD`; otherwise leave the placeholder text exactly as `CLOUDP-TBD` so it's easy to grep for later — do not invent a ticket number.

- [ ] **Step 2: Full repo build, vet, and test**

Run: `cd /Users/maciej.karas/mongodb/mongodb-kubernetes && go build -mod=mod ./... && go vet -mod=mod ./... && go test -mod=mod ./...`
Expected: all clean. This is the final regression gate for the whole plan — if anything fails here that wasn't caught by the per-task test runs, fix it before considering the plan complete.

- [ ] **Step 3: Commit**

```bash
git add controllers/operator/construct/opsmanager_construction.go controllers/operator/mongodbopsmanager_controller.go
git commit -m "docs: clarify deferred TLS/CA AppDB TODOs point to follow-up PR"
```

---

## Post-plan note (not a task — informational)

`detachInternalAppDB` (mongodbopsmanager_controller.go, called from `ExternalAppDBReconciler.ReconcileAppDB`) still calls `r.createNewAppDBReconciler` internally to get `GetHealthyMemberClusters()` for the pre-existing internal AppDB it's detaching. This is intentional and out of scope for this plan — per your explicit direction, `OnDelete`'s equivalent pattern is left calling the internal reconciler directly, and `detachInternalAppDB` is the same class of "one-time migration step that legitimately needs to inspect the old internal AppDB's topology," not a violation of the "ExternalAppDBReconciler never touches Spec.AppDB" invariant (the invariant applies to `ExternalAppDBReconciler`'s own methods, not to `OpsManagerReconciler` helper methods it happens to call).
