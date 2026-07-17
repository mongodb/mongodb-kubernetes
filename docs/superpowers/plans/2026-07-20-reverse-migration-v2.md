# Reverse Migration v2 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace deletion-triggered reverse migration with an annotation handshake (CR stays alive through the handover), plain-deletion fallback with retained PVCs, and handover-following secret ownership — per `docs/superpowers/specs/2026-07-20-reverse-migration-v2-design.md`.

**Architecture:** The OM's internal AppDB reconciler becomes the ownership arbiter for the AppDB StatefulSet (`ensureAppDBStatefulSetOwnership` state machine: owned / not-found / foreign→request release / ownerless→adopt). The MongoDB CR reconciler answers a release request by stripping its OwnerReference. The `appdb-detach` finalizer and detach-on-delete machinery are deleted. The shared `-om-password`/`-keyfile` secrets are claimed by whichever side currently manages the AppDB.

**Tech Stack:** Go (controller-runtime, fake client, table-driven tests), pytest e2e.

## Global Constraints

- Annotation names: `mongodb.com/appdb-migration-ready` (forward, unchanged), `mongodb.com/appdb-reverse-migration-ready` (new)
- Pending message (OM side, waiting): `waiting for MongoDB controller to release AppDB StatefulSet`
- Pending message (CR side, released): `released AppDB StatefulSet to Ops Manager; this resource can be deleted`
- Shared secret names: `<om>-db-om-password` (`omv1.OpsManagerUserPasswordSecretName`), `<om>-db-keyfile` (`AppDBSpec.GetAgentKeyfileSecretNamespacedName`)
- After every task: `go build ./...` and the named test commands must pass; run `make precommit` once at the end (Task 8)
- All work on the current branch; commit per task, messages given per task

---

### Task 1: OM-side ownership state machine (replaces reAdoptInternalAppDB*)

**Files:**
- Modify: `controllers/operator/appdbreplicaset_controller.go` (consts near `appDBMigrationReadyAnnotation`; replace `reAdoptInternalAppDB` + `reAdoptInternalAppDBIfNeeded`; call site at top of `ReconcileAppDB`)
- Test: `controllers/operator/appdbreplicaset_controller_test.go` (replace `TestReAdoptInternalAppDBIfNeeded`)

**Interfaces:**
- Produces: `const appDBReverseMigrationReadyAnnotation = "mongodb.com/appdb-reverse-migration-ready"`; `func (r *ReconcileAppDbReplicaSet) ensureAppDBStatefulSetOwnership(ctx context.Context, opsManager *omv1.MongoDBOpsManager) (bool, error)` — returns `(owned, err)`; `owned == false` means a release was requested and the caller must return Pending
- Consumes: existing `appDBMigrationReadyAnnotation`, `trueString`, `kube.BaseOwnerReference`

- [ ] **Step 1: Replace the gate unit test with the new state machine table**

Replace `TestReAdoptInternalAppDBIfNeeded` in `appdbreplicaset_controller_test.go` with:

```go
func TestEnsureAppDBStatefulSetOwnership(t *testing.T) {
	const crUID = "cr-uid-2222"

	tests := []struct {
		name string
		// sts builds the pre-existing AppDB StatefulSet; nil means it doesn't exist
		sts                        func(testOm *omv1.MongoDBOpsManager) appsv1.StatefulSet
		omUID                      types.UID
		expectedOwned              bool
		expectedOMOwnerRef         bool
		expectedReverseAnnotation  bool
		expectedForwardAnnotation  bool
	}{
		{
			name:          "StatefulSet absent: recreate-from-scratch path proceeds",
			expectedOwned: true,
		},
		{
			name:  "already OM-owned: proceeds untouched",
			omUID: "om-uid-1111",
			sts: func(testOm *omv1.MongoDBOpsManager) appsv1.StatefulSet {
				return DefaultStatefulSetBuilder().SetName(testOm.Spec.AppDB.Name()).
					SetOwnerReferences(kube.BaseOwnerReference(testOm)).Build()
			},
			expectedOwned:      true,
			expectedOMOwnerRef: true,
		},
		{
			name:  "CR-owned: requests release and blocks",
			omUID: "om-uid-1111",
			sts: func(testOm *omv1.MongoDBOpsManager) appsv1.StatefulSet {
				return DefaultStatefulSetBuilder().SetName(testOm.Spec.AppDB.Name()).
					SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "mongodb.com/v1", Kind: "MongoDB", Name: "test-om-db", UID: crUID}}).Build()
			},
			expectedOwned:             false,
			expectedReverseAnnotation: true,
		},
		{
			name:  "ownerless with release request: adopts and clears annotations",
			omUID: "om-uid-1111",
			sts: func(testOm *omv1.MongoDBOpsManager) appsv1.StatefulSet {
				return DefaultStatefulSetBuilder().SetName(testOm.Spec.AppDB.Name()).
					SetOwnerReferences(nil).
					SetAnnotations(map[string]string{appDBReverseMigrationReadyAnnotation: "true"}).Build()
			},
			expectedOwned:      true,
			expectedOMOwnerRef: true,
		},
		{
			name:  "ownerless with stale forward annotation: adopts and clears it",
			omUID: "om-uid-1111",
			sts: func(testOm *omv1.MongoDBOpsManager) appsv1.StatefulSet {
				return DefaultStatefulSetBuilder().SetName(testOm.Spec.AppDB.Name()).
					SetOwnerReferences(nil).
					SetAnnotations(map[string]string{appDBMigrationReadyAnnotation: "true"}).Build()
			},
			expectedOwned:      true,
			expectedOMOwnerRef: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			testOm := DefaultOpsManagerBuilder().SetName("test-om").Build()
			testOm.UID = tt.omUID

			kubeClient, omConnectionFactory := mock.NewDefaultFakeClient(testOm)
			reconciler, err := newAppDbReconciler(ctx, kubeClient, testOm, omConnectionFactory.GetConnectionFunc, zap.S())
			require.NoError(t, err)
			if tt.sts != nil {
				sts := tt.sts(testOm)
				require.NoError(t, kubeClient.Create(ctx, &sts))
			}

			owned, err := reconciler.ensureAppDBStatefulSetOwnership(ctx, testOm)
			require.NoError(t, err)
			assert.Equal(t, tt.expectedOwned, owned)

			if tt.sts != nil {
				result := appsv1.StatefulSet{}
				require.NoError(t, kubeClient.Get(ctx, kube.ObjectKey(testOm.Namespace, testOm.Spec.AppDB.Name()), &result))
				hasOMRef := false
				for _, ref := range result.OwnerReferences {
					if ref.UID == testOm.UID {
						hasOMRef = true
					}
				}
				assert.Equal(t, tt.expectedOMOwnerRef, hasOMRef)
				assert.Equal(t, tt.expectedReverseAnnotation, result.Annotations[appDBReverseMigrationReadyAnnotation] == "true")
				assert.Equal(t, tt.expectedForwardAnnotation, result.Annotations[appDBMigrationReadyAnnotation] == "true")
			}
		})
	}
}
```

- [ ] **Step 2: Run to verify it fails** — `go test -count=1 -run TestEnsureAppDBStatefulSetOwnership ./controllers/operator/` — expected: FAIL (`ensureAppDBStatefulSetOwnership` / `appDBReverseMigrationReadyAnnotation` undefined)

- [ ] **Step 3: Implement the state machine**

In `appdbreplicaset_controller.go`, next to `appDBMigrationReadyAnnotation`, add the const; replace `reAdoptInternalAppDB` and `reAdoptInternalAppDBIfNeeded` with:

```go
// appDBReverseMigrationReadyAnnotation is the reverse-migration release request: set by the
// internal AppDB reconciler on a StatefulSet still owned by a MongoDB CR, answered by the
// MongoDB controller stripping its OwnerReference, and removed here at adoption. Removal happens
// at adoption (not at migration completion), symmetric with the forward direction's
// consumeAdoptionSignal: from adoption onward the OwnerReference is the authoritative state.
const appDBReverseMigrationReadyAnnotation = "mongodb.com/appdb-reverse-migration-ready"

// ensureAppDBStatefulSetOwnership arbitrates ownership of the AppDB StatefulSet at the start of
// every internal reconcile (reverse migration v2, see
// docs/superpowers/specs/2026-07-20-reverse-migration-v2-design.md):
//   - absent: nothing to own - the reconcile creates it from scratch (retained PVCs re-bind by name)
//   - owned by this OM: proceed
//   - foreign-owned (a MongoDB CR): request release via appDBReverseMigrationReadyAnnotation and block
//   - ownerless: adopt - set this OM's OwnerReference and clear both migration annotations
func (r *ReconcileAppDbReplicaSet) ensureAppDBStatefulSetOwnership(ctx context.Context, opsManager *omv1.MongoDBOpsManager) (bool, error) {
	stsKey := kube.ObjectKey(opsManager.Namespace, opsManager.Spec.AppDB.Name())
	sts := appsv1.StatefulSet{}
	if err := r.client.Get(ctx, stsKey, &sts); err != nil {
		if apiErrors.IsNotFound(err) {
			return true, nil
		}
		return false, xerrors.Errorf("failed to fetch StatefulSet during ownership check: %w", err)
	}

	for _, ref := range sts.OwnerReferences {
		if ref.UID == opsManager.UID {
			return true, nil
		}
	}

	if len(sts.OwnerReferences) > 0 {
		if sts.Annotations[appDBReverseMigrationReadyAnnotation] == trueString {
			return false, nil // release already requested, keep waiting
		}
		if sts.Annotations == nil {
			sts.Annotations = map[string]string{}
		}
		sts.Annotations[appDBReverseMigrationReadyAnnotation] = trueString
		if err := r.client.Update(ctx, &sts); err != nil {
			return false, xerrors.Errorf("failed to request StatefulSet release: %w", err)
		}
		return false, nil
	}

	sts.OwnerReferences = kube.BaseOwnerReference(opsManager)
	delete(sts.Annotations, appDBReverseMigrationReadyAnnotation)
	delete(sts.Annotations, appDBMigrationReadyAnnotation)
	if err := r.client.Update(ctx, &sts); err != nil {
		return false, xerrors.Errorf("failed to adopt StatefulSet: %w", err)
	}

	return true, nil
}
```

Replace the call site at the top of `ReconcileAppDB` (currently calling `reAdoptInternalAppDBIfNeeded`):

```go
	owned, err := r.ensureAppDBStatefulSetOwnership(ctx, opsManager)
	if err != nil {
		return r.updateStatus(ctx, opsManager, workflow.Failed(err), log, appDbStatusOption)
	}
	if !owned {
		return r.updateStatus(ctx, opsManager, workflow.Pending("waiting for MongoDB controller to release AppDB StatefulSet"), log, appDbStatusOption)
	}
```

- [ ] **Step 4: Run tests** — `go test -count=1 -run "TestEnsureAppDBStatefulSetOwnership|TestReconcileAppDB_ReshapesReAdoptedStatefulSet" ./controllers/operator/` — expected: PASS (the reshape test's fixture is OM-owned, unaffected)
- [ ] **Step 5: Commit** — `git add -A controllers/operator/ && git commit -m "feat: replace re-adoption gate with AppDB StatefulSet ownership state machine"`

---

### Task 2: OM claims shared secrets at adoption

**Files:**
- Modify: `controllers/operator/appdbreplicaset_controller.go` (inside `ensureAppDBStatefulSetOwnership`'s adopt branch)
- Test: `controllers/operator/appdbreplicaset_controller_test.go`

**Interfaces:**
- Produces: `func (r *ReconcileAppDbReplicaSet) claimAppDBSecrets(ctx context.Context, opsManager *omv1.MongoDBOpsManager) error`
- Consumes: `omv1.OpsManagerUserPasswordSecretName(...)`, `opsManager.Spec.AppDB.GetAgentKeyfileSecretNamespacedName()`

- [ ] **Step 1: Write the failing test**

```go
func TestEnsureAppDBStatefulSetOwnership_ClaimsSharedSecretsOnAdoption(t *testing.T) {
	ctx := context.Background()
	testOm := DefaultOpsManagerBuilder().SetName("test-om").Build()
	testOm.UID = types.UID("om-uid-1111")

	kubeClient, omConnectionFactory := mock.NewDefaultFakeClient(testOm)
	reconciler, err := newAppDbReconciler(ctx, kubeClient, testOm, omConnectionFactory.GetConnectionFunc, zap.S())
	require.NoError(t, err)

	sts := DefaultStatefulSetBuilder().SetName(testOm.Spec.AppDB.Name()).
		SetOwnerReferences(nil).
		SetAnnotations(map[string]string{appDBReverseMigrationReadyAnnotation: "true"}).Build()
	require.NoError(t, kubeClient.Create(ctx, &sts))

	crOwnerRef := []metav1.OwnerReference{{APIVersion: "mongodb.com/v1", Kind: "MongoDB", Name: "test-om-db", UID: "cr-uid-2222"}}
	for _, name := range []string{omv1.OpsManagerUserPasswordSecretName("test-om-db"), testOm.Spec.AppDB.GetAgentKeyfileSecretNamespacedName().Name} {
		s := secret.Builder().SetName(name).SetNamespace(testOm.Namespace).SetField("k", "v").SetOwnerReferences(crOwnerRef).Build()
		require.NoError(t, kubeClient.CreateSecret(ctx, s))
	}

	owned, err := reconciler.ensureAppDBStatefulSetOwnership(ctx, testOm)
	require.NoError(t, err)
	require.True(t, owned)

	for _, name := range []string{omv1.OpsManagerUserPasswordSecretName("test-om-db"), testOm.Spec.AppDB.GetAgentKeyfileSecretNamespacedName().Name} {
		s := corev1.Secret{}
		require.NoError(t, kubeClient.Get(ctx, kube.ObjectKey(testOm.Namespace, name), &s))
		require.Len(t, s.OwnerReferences, 1, name)
		assert.Equal(t, testOm.UID, s.OwnerReferences[0].UID, "secret %s must be claimed by the OM at adoption", name)
	}
}
```

- [ ] **Step 2: Run to verify it fails** — `go test -count=1 -run TestEnsureAppDBStatefulSetOwnership_ClaimsSharedSecretsOnAdoption ./controllers/operator/` — expected: FAIL (secrets keep the CR ownerRef)
- [ ] **Step 3: Implement**

Add to `appdbreplicaset_controller.go` and call it in the adopt branch of `ensureAppDBStatefulSetOwnership`, immediately after the StatefulSet update succeeds:

```go
// claimAppDBSecrets transfers the shared handover secrets (password, keyfile) to this OM's
// ownership at adoption, so the eventual post-handover deletion of the MongoDB CR doesn't
// garbage-collect secrets the running internal AppDB depends on (ownership follows the AppDB's
// manager - see the reverse-migration v2 design).
func (r *ReconcileAppDbReplicaSet) claimAppDBSecrets(ctx context.Context, opsManager *omv1.MongoDBOpsManager) error {
	names := []string{
		omv1.OpsManagerUserPasswordSecretName(opsManager.Spec.AppDB.Name()),
		opsManager.Spec.AppDB.GetAgentKeyfileSecretNamespacedName().Name,
	}
	for _, name := range names {
		s := corev1.Secret{}
		if err := r.client.Get(ctx, kube.ObjectKey(opsManager.Namespace, name), &s); err != nil {
			if apiErrors.IsNotFound(err) {
				continue // recreated later by the normal reconcile path
			}
			return xerrors.Errorf("failed to fetch secret %s while claiming ownership: %w", name, err)
		}
		s.OwnerReferences = kube.BaseOwnerReference(opsManager)
		if err := r.client.Update(ctx, &s); err != nil {
			return xerrors.Errorf("failed to claim secret %s: %w", name, err)
		}
	}
	return nil
}
```

In the adopt branch:

```go
	if err := r.claimAppDBSecrets(ctx, opsManager); err != nil {
		return false, err
	}
```

- [ ] **Step 4: Run tests** — `go test -count=1 -run "TestEnsureAppDBStatefulSetOwnership" ./controllers/operator/` — expected: PASS (both tests)
- [ ] **Step 5: Commit** — `git commit -am "feat: OM claims shared AppDB secrets at reverse-migration adoption"`

---

### Task 3: CR-side release step + defensive gate condition

**Files:**
- Modify: `controllers/operator/mongodbreplicaset_controller.go` (new `releaseStatefulSetIfRequested`; wire into the `role: AppDB` block of `Reconcile` before `checkAdoptionGate`; extend `checkAdoptionGate`)
- Test: `controllers/operator/mongodbreplicaset_controller_test.go`

**Interfaces:**
- Produces: `func (r *ReplicaSetReconcilerHelper) releaseStatefulSetIfRequested(ctx context.Context, mdb *mdbv1.MongoDB) (bool, error)`
- Consumes: `appDBReverseMigrationReadyAnnotation` (Task 1), `appDBMigrationReadyAnnotation`, `trueString`

- [ ] **Step 1: Write the failing tests**

```go
func TestReleaseStatefulSetIfRequested(t *testing.T) {
	tests := []struct {
		name              string
		annotations       map[string]string
		crOwned           bool
		expectedReleased  bool
		expectedOwnerRefs int
	}{
		{
			name:              "release requested on owned StatefulSet: strips ownerRef, keeps annotation",
			annotations:       map[string]string{appDBReverseMigrationReadyAnnotation: "true"},
			crOwned:           true,
			expectedReleased:  true,
			expectedOwnerRefs: 0,
		},
		{
			name:              "release requested on already-released StatefulSet: stays released",
			annotations:       map[string]string{appDBReverseMigrationReadyAnnotation: "true"},
			expectedReleased:  true,
			expectedOwnerRefs: 0,
		},
		{
			name:              "no release request: untouched",
			crOwned:           true,
			expectedReleased:  false,
			expectedOwnerRefs: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			mdb := DefaultReplicaSetBuilder().SetName("my-om-db").SetRole(mdbv1.RoleAppDB).Build()
			mdb.UID = types.UID("cr-uid-2222") // UID matching requires non-empty UIDs
			reconciler, kubeClient, _ := defaultReplicaSetReconciler(ctx, nil, "", "", mdb, architectures.NonStatic)
			helper := &ReplicaSetReconcilerHelper{resource: mdb, reconciler: reconciler, log: zap.S()}

			var refs []metav1.OwnerReference
			if tt.crOwned {
				refs = kube.BaseOwnerReference(mdb)
			}
			sts := DefaultStatefulSetBuilder().SetName(mdb.Name).SetOwnerReferences(refs).SetAnnotations(tt.annotations).Build()
			require.NoError(t, kubeClient.Create(ctx, &sts))

			released, err := helper.releaseStatefulSetIfRequested(ctx, mdb)
			require.NoError(t, err)
			assert.Equal(t, tt.expectedReleased, released)

			result := appsv1.StatefulSet{}
			require.NoError(t, kubeClient.Get(ctx, kube.ObjectKey(mdb.Namespace, mdb.Name), &result))
			assert.Len(t, result.OwnerReferences, tt.expectedOwnerRefs)
			if tt.expectedReleased {
				assert.Equal(t, "true", result.Annotations[appDBReverseMigrationReadyAnnotation],
					"the release request must stay on the StatefulSet until the OM adopts it")
			}
		})
	}
}

func TestAdoptionGate_BlocksWhileReleaseRequested(t *testing.T) {
	// defensive condition: a stale, unconsumed appdb-migration-ready must not let the CR
	// re-adopt a StatefulSet the OM has requested released
	ctx := context.Background()
	sts := DefaultStatefulSetBuilder().SetName("my-om-db").
		SetOwnerReferences(nil).
		SetAnnotations(map[string]string{
			appDBMigrationReadyAnnotation:        "true",
			appDBReverseMigrationReadyAnnotation: "true",
		}).Build()
	mdb := DefaultReplicaSetBuilder().SetName("my-om-db").SetRole(mdbv1.RoleAppDB).Build()
	reconciler, kubeClient, _ := defaultReplicaSetReconciler(ctx, nil, "", "", mdb, architectures.NonStatic)
	require.NoError(t, kubeClient.Create(ctx, &sts))
	helper := &ReplicaSetReconcilerHelper{resource: mdb, reconciler: reconciler, log: zap.S()}

	blocked, err := helper.checkAdoptionGate(ctx, mdb)
	assert.NoError(t, err)
	assert.True(t, blocked)
}
```

- [ ] **Step 2: Run to verify failure** — `go test -count=1 -run "TestReleaseStatefulSetIfRequested|TestAdoptionGate_BlocksWhileReleaseRequested" ./controllers/operator/` — expected: FAIL (method undefined / gate passes)
- [ ] **Step 3: Implement**

```go
// releaseStatefulSetIfRequested answers a reverse-migration release request: when the OM's
// internal AppDB reconciler has set appDBReverseMigrationReadyAnnotation on this CR's
// StatefulSet, strip this CR's OwnerReference (the annotation stays until the OM adopts).
// Returns true while the release request is in effect - the caller must stop managing the
// StatefulSet and report the released state.
func (r *ReplicaSetReconcilerHelper) releaseStatefulSetIfRequested(ctx context.Context, mdb *mdbv1.MongoDB) (bool, error) {
	sts := appsv1.StatefulSet{}
	err := r.reconciler.client.Get(ctx, kube.ObjectKey(mdb.Namespace, mdb.Name), &sts)
	if errors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, xerrors.Errorf("failed to fetch StatefulSet during release check: %w", err)
	}

	if sts.Annotations[appDBReverseMigrationReadyAnnotation] != trueString {
		return false, nil
	}

	remaining := make([]metav1.OwnerReference, 0, len(sts.OwnerReferences))
	for _, ref := range sts.OwnerReferences {
		if ref.UID != mdb.UID {
			remaining = append(remaining, ref)
		}
	}
	if len(remaining) != len(sts.OwnerReferences) {
		sts.OwnerReferences = remaining
		if err := r.reconciler.client.Update(ctx, &sts); err != nil {
			return false, xerrors.Errorf("failed to release StatefulSet: %w", err)
		}
	}
	return true, nil
}
```

Wire into `Reconcile`'s `role: AppDB` block, before `checkAdoptionGate`:

```go
	if rs.Spec.Role == mdbv1.RoleAppDB {
		released, err := r.releaseStatefulSetIfRequested(ctx, rs)
		if err != nil {
			return r.updateStatus(ctx, workflow.Failed(err))
		}
		if released {
			return r.updateStatus(ctx, workflow.Pending("released AppDB StatefulSet to Ops Manager; this resource can be deleted"))
		}
		// ... existing checkAdoptionGate + consumeAdoptionSignal ...
	}
```

Extend `checkAdoptionGate` (after the own-UID short-circuit loop):

```go
	if sts.Annotations[appDBReverseMigrationReadyAnnotation] == trueString {
		return true, nil // OM requested a release; never adopt while the request stands
	}
```

- [ ] **Step 4: Run tests** — `go test -count=1 -run "TestReleaseStatefulSetIfRequested|TestAdoptionGate" ./controllers/operator/` — expected: PASS
- [ ] **Step 5: Commit** — `git commit -am "feat: MongoDB controller releases AppDB StatefulSet on reverse-migration request"`

---

### Task 4: CR claims shared secrets; forward detach strips keyfile secret + clears stale release request

**Files:**
- Modify: `controllers/operator/mongodbreplicaset_controller.go` (`ensureAppDBRoleUser`, `ensureAppDBRoleKeyfile`)
- Modify: `controllers/operator/mongodbopsmanager_controller.go` (`stripInternalAppDBOwnerReferencesFromSecretsAndConfigMaps`)
- Modify: `controllers/operator/external_appdb_reconciler.go` (`detachInternalAppDB`)
- Test: `controllers/operator/mongodbreplicaset_controller_test.go`, `controllers/operator/external_appdb_reconciler_test.go`

**Interfaces:**
- Produces: `func (r *ReplicaSetReconcilerHelper) claimSecretForCR(ctx context.Context, mdb *mdbv1.MongoDB, name string) error`
- Consumes: strip helper `stripOwnerReferenceFromSecret` (existing, mongodbopsmanager_controller.go)

- [ ] **Step 1: Write the failing tests**

In `mongodbreplicaset_controller_test.go` (extends existing keyfile/user tests):

```go
func TestEnsureAppDBRoleSecrets_ClaimedByCR(t *testing.T) {
	// forward migration: the secrets pre-exist (created by internal AppDB, ownerRefs stripped by
	// detach); the CR must claim them so its eventual deletion (fallback path) GCs them together
	// with the StatefulSet
	ctx := context.Background()
	mdb := DefaultReplicaSetBuilder().SetName("my-om-db").SetRole(mdbv1.RoleAppDB).Build()
	mdb.UID = types.UID("cr-uid-2222")
	reconciler, kubeClient, omConnectionFactory := defaultReplicaSetReconciler(ctx, nil, "", "", mdb, architectures.NonStatic)
	helper := &ReplicaSetReconcilerHelper{resource: mdb, reconciler: reconciler, log: zap.S()}
	conn := omConnectionFactory.GetConnectionFunc(&om.OMContext{GroupName: om.TestGroupName})

	passwordName := omv1.OpsManagerUserPasswordSecretName(mdb.Name)
	keyfileName := fmt.Sprintf("%s-keyfile", mdb.Name)
	for name, field := range map[string]string{passwordName: util.OpsManagerPasswordKey, keyfileName: constants.AgentKeyfileKey} {
		s := secret.Builder().SetName(name).SetNamespace(mdb.Namespace).SetField(field, "pre-existing").Build()
		require.NoError(t, kubeClient.CreateSecret(ctx, s))
	}

	require.NoError(t, helper.ensureAppDBRoleUser(ctx, mdb, conn))
	require.NoError(t, helper.ensureAppDBRoleKeyfile(ctx, mdb, conn))

	for _, name := range []string{passwordName, keyfileName} {
		s := corev1.Secret{}
		require.NoError(t, kubeClient.Get(ctx, kube.ObjectKey(mdb.Namespace, name), &s))
		require.Len(t, s.OwnerReferences, 1, name)
		assert.Equal(t, mdb.UID, s.OwnerReferences[0].UID, "secret %s must be claimed by the CR", name)
	}
}
```

In `external_appdb_reconciler_test.go`, extend `TestDetachInternalAppDB_OnlyDetachesOMOwnedStatefulSet`'s OM-owned case fixture with a keyfile secret (same `originalOwnerRefs`) and, in the `expectedDetached` branch, assert its ownerRefs are stripped; in the non-detached branches assert they're preserved. Also add:

```go
func TestDetachInternalAppDB_ClearsStaleReverseMigrationRequest(t *testing.T) {
	// abort path: user re-added the ref while a reverse migration was in flight; the ownerless
	// StatefulSet must be handed to the CR (annotation swap), exactly like a forward detach state
	ctx := context.Background()
	testOm := withExternalAppDBRef(DefaultOpsManagerBuilder().SetName("test-om").Build(), validExternalAppDBRef())
	testOm.UID = types.UID("om-uid-1111")
	mdb := validExternalAppDBMongoDB()

	sts := appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "test-om-db",
			Namespace:   mock.TestNamespace,
			Annotations: map[string]string{appDBReverseMigrationReadyAnnotation: "true"},
		},
		Spec: appsv1.StatefulSetSpec{Replicas: ptr.To(int32(3))},
	}

	omConnectionFactory := om.NewDefaultCachedOMConnectionFactory()
	reconciler, kubeClient, _ := defaultTestOmReconciler(ctx, t, nil, "", "", testOm, nil, omConnectionFactory, architectures.NonStatic)
	require.NoError(t, reconciler.client.Create(ctx, mdb))
	require.NoError(t, reconciler.client.Create(ctx, &sts))

	require.NoError(t, reconciler.createNewExternalAppDBReconciler(zap.S()).detachInternalAppDB(ctx, testOm, zap.S()))

	result := appsv1.StatefulSet{}
	require.NoError(t, kubeClient.Get(ctx, kube.ObjectKey(testOm.Namespace, "test-om-db"), &result))
	assert.NotContains(t, result.Annotations, appDBReverseMigrationReadyAnnotation)
	assert.Equal(t, "true", result.Annotations[appDBMigrationReadyAnnotation],
		"ownerless StatefulSet must be handed to the CR via the forward-adoption annotation")
}
```

- [ ] **Step 2: Run to verify failure** — `go test -count=1 -run "TestEnsureAppDBRoleSecrets_ClaimedByCR|TestDetachInternalAppDB" ./controllers/operator/` — expected: FAIL
- [ ] **Step 3: Implement**

(a) `mongodbreplicaset_controller.go` — claim helper + calls. In `ensureAppDBRoleUser`, the existing-password branch currently does nothing; in `ensureAppDBRoleKeyfile`, the existing-secret branch likewise. Add:

```go
// claimSecretForCR sets this CR's OwnerReference on a shared handover secret it did not create
// (forward migration: created by internal AppDB, OM refs stripped by detach). Ownership follows
// the AppDB's manager; on plain CR deletion the secret is then garbage-collected together with
// the StatefulSet, and the recreate-from-scratch path regenerates credentials uniformly.
func (r *ReplicaSetReconcilerHelper) claimSecretForCR(ctx context.Context, mdb *mdbv1.MongoDB, name string) error {
	s := corev1.Secret{}
	if err := r.reconciler.client.Get(ctx, kube.ObjectKey(mdb.Namespace, name), &s); err != nil {
		return xerrors.Errorf("failed to fetch secret %s while claiming ownership: %w", name, err)
	}
	for _, ref := range s.OwnerReferences {
		if ref.UID == mdb.UID {
			return nil
		}
	}
	s.OwnerReferences = kube.BaseOwnerReference(mdb)
	if err := r.reconciler.client.Update(ctx, &s); err != nil {
		return xerrors.Errorf("failed to claim secret %s: %w", name, err)
	}
	return nil
}
```

In `ensureAppDBRoleUser`: in the branch where the password already exists (`password != ""`), call `claimSecretForCR(ctx, mdb, secretName)`. In `ensureAppDBRoleKeyfile`: in the `sharedKey != ""` branch, call `claimSecretForCR(ctx, mdb, secretName)`.

(b) `mongodbopsmanager_controller.go` — in `stripInternalAppDBOwnerReferencesFromSecretsAndConfigMaps`, next to the password-secret strip:

```go
	if err := stripOwnerReferenceFromSecret(ctx, r.client, opsManager.Namespace, appDBSpec.GetAgentKeyfileSecretNamespacedName().Name); err != nil {
		return err
	}
```

(c) `external_appdb_reconciler.go` — in `detachInternalAppDB`, replace the `if !ownedByThisOM { return nil }` block:

```go
	if !ownedByThisOM {
		// Abort of an in-flight reverse migration: the internal reconciler requested a release
		// (and the CR may already have complied). Hand the StatefulSet to the CR by swapping the
		// annotations - removing the request alone would leave the CR's gate blocked forever.
		if sts.Annotations[appDBReverseMigrationReadyAnnotation] == trueString {
			delete(sts.Annotations, appDBReverseMigrationReadyAnnotation)
			if len(sts.OwnerReferences) == 0 {
				sts.Annotations[appDBMigrationReadyAnnotation] = trueString
			}
			if err := e.client.Update(ctx, &sts); err != nil {
				return xerrors.Errorf("failed to clear reverse-migration request from StatefulSet %s: %w", stsKey.Name, err)
			}
		}
		return nil // Fresh Start (StatefulSet belongs to the referenced CR) or detach already completed
	}
```

Also, in the detach (OM-owned) branch, add `delete(sts.Annotations, appDBReverseMigrationReadyAnnotation)` alongside setting `appDBMigrationReadyAnnotation` (defensive).

- [ ] **Step 4: Run tests** — `go test -count=1 -run "TestEnsureAppDBRoleSecrets_ClaimedByCR|TestDetachInternalAppDB|TestEnsureAppDBRoleKeyfile|TestEnsureAppDBRoleUser" ./controllers/operator/` — expected: PASS
- [ ] **Step 5: Commit** — `git commit -am "feat: handover-following secret ownership + reverse-migration abort path"`

---

### Task 5: Delete the finalizer / detach-on-delete machinery

**Files:**
- Modify: `controllers/operator/mongodbreplicaset_controller.go` — delete `appDBDetachFinalizer` const, `ensureAppDBFinalizer`, `cleanupAppDBFinalizer`, the deletion-timestamp branch at the top of `Reconcile` (`if !rs.DeletionTimestamp.IsZero() && rs.Spec.Role == mdbv1.RoleAppDB && controllerutil.ContainsFinalizer(...)`), and the `ensureAppDBFinalizer` call in `Reconcile`
- Test: `controllers/operator/mongodbreplicaset_controller_test.go` — delete `TestAppDBFinalizer_*` and finalizer-cleanup tests; keep `TestOnDelete_AppDBRoleSkipsOpsManagerCleanup` (OnDelete still skips `cleanOpsManagerState` for role CRs — decided: never clean)

- [ ] **Step 1: Delete the code** (const, two methods, two call sites) and the corresponding tests. Remove the now-unused `controllerutil` import if nothing else uses it.
- [ ] **Step 2: Run the full operator suite** — `go test -count=1 ./controllers/operator/` — expected: PASS, no references to the deleted symbols (`rg -n "appDBDetachFinalizer" controllers/` returns nothing)
- [ ] **Step 3: Commit** — `git commit -am "refactor: remove appdb-detach finalizer; CR deletion is plain deletion (reverse migration v2)"`

---

### Task 6: E2E — fresh file reverse class on the graceful flow

**Files:**
- Modify: `docker/mongodb-kubernetes-tests/tests/opsmanager/om_external_appdb_fresh.py` (`TestReverseMigrationAfterFreshStart`)

- [ ] **Step 1: Rewrite the class to the v2 sequence** (module fixtures unchanged):

```python
@pytest.mark.e2e_om_external_appdb_fresh
class TestReverseMigrationAfterFreshStart:
    """Procedure 3 v2 (graceful), continuing from TestFreshStartExternalAppDB's end state: the
    MongoDB CR is NOT deleted to start the migration. Reconfiguring the OM (remove ref, add
    spec.applicationDatabase) triggers the release handshake; the CR is deleted only after the
    handover completes, and must not disturb anything the OM now owns."""

    password_secret_before: ClassVar[dict[str, str]]
    keyfile_secret_uid: ClassVar[str]

    def test_write_sentinel_doc(self, primary_om: MongoDBOpsManager):
        write_sentinel_doc(primary_om.read_appdb_connection_url())

    def test_capture_secrets_before_reverse_migration(self, namespace: str):
        self.__class__.password_secret_before = read_secret(namespace, password_secret_name(OM_NAME))
        sec = k8s_client.CoreV1Api().read_namespaced_secret(f"{DB_NAME}-keyfile", namespace)
        self.__class__.keyfile_secret_uid = sec.metadata.uid

    def test_reverse_migration_reconfigure_om(self, primary_om: MongoDBOpsManager, custom_appdb_version: str):
        # v2: the MongoDB CR stays; reconfiguring the OM alone starts the handover.
        # update() sends a JSON merge patch: only an explicit null removes a field
        primary_om.load()
        primary_om["spec"]["externalApplicationDatabaseRef"] = None
        primary_om["spec"]["applicationDatabase"] = {"members": 3, "version": custom_appdb_version}
        primary_om.update()

    def test_mongodb_cr_reaches_released_state(self, external_appdb: MongoDB):
        external_appdb.assert_reaches_phase(
            Phase.Pending, msg_regexp=".*released AppDB StatefulSet to Ops Manager.*", timeout=300
        )

    def test_internal_appdb_management_resumes(self, primary_om: MongoDBOpsManager):
        primary_om.appdb_status().assert_reaches_phase(Phase.Running, timeout=900)
        primary_om.om_status().assert_reaches_phase(Phase.Running, timeout=900)

    def test_delete_mongodb_cr_after_handover(self, external_appdb: MongoDB, namespace: str):
        external_appdb.delete()

        def cr_is_gone():
            try:
                k8s_client.CustomObjectsApi().get_namespaced_custom_object(
                    "mongodb.com", "v1", namespace, "mongodb", DB_NAME
                )
                return False
            except ApiException as e:
                if e.status == 404:
                    return True
                raise

        KubernetesTester.wait_until(cr_is_gone, timeout=300)

    def test_om_untouched_by_cr_deletion(self, primary_om: MongoDBOpsManager, namespace: str):
        # the OM-claimed secrets must survive the CR deletion (same object, not recreated)
        sec = k8s_client.CoreV1Api().read_namespaced_secret(f"{DB_NAME}-keyfile", namespace)
        assert sec.metadata.uid == self.keyfile_secret_uid
        primary_om.appdb_status().assert_reaches_phase(Phase.Running, timeout=300)

    def test_sentinel_doc_survives_reverse_migration(self, primary_om: MongoDBOpsManager):
        assert_sentinel_doc_present(primary_om.read_appdb_connection_url())

    def test_password_secret_unchanged_after_reverse_migration(self, namespace: str):
        # graceful-path property: shared password identical across the whole handover
        password_secret_now = read_secret(namespace, password_secret_name(OM_NAME))
        assert password_secret_now == self.password_secret_before
```

- [ ] **Step 2: Verify** — `python3 -m py_compile tests/opsmanager/om_external_appdb_fresh.py` and `venv pytest --collect-only -q tests/opsmanager/om_external_appdb_fresh.py` — expected: clean collection
- [ ] **Step 3: Commit** — `git commit -am "test: fresh-suite reverse migration uses the v2 delete-free handshake"`

---

### Task 7: E2E — forward file reverse class becomes the fallback (delete-first) test

**Files:**
- Modify: `docker/mongodb-kubernetes-tests/tests/opsmanager/om_external_appdb_forward.py` (`TestReverseMigrationAfterForwardMigration`)

- [ ] **Step 1: Rewrite to the fallback sequence**: delete the CR first (plain deletion — no finalizer now, assert CR gone within 300s and STS gone), tolerate the OM Failed window, then reconfigure the OM (ref → None; `applicationDatabase` already in spec), assert internal AppDB reaches Running (recreate re-binds PVCs), and assert the **sentinel document survives** via `assert_sentinel_doc_present`. Drop the `password_secret_before` capture and both password-unchanged assertions in this class (credential rotation is an accepted property of the fallback path — class docstring updated accordingly).
- [ ] **Step 2: Verify** — `py_compile` + `pytest --collect-only` on the file — expected: clean
- [ ] **Step 3: Commit** — `git commit -am "test: forward-suite reverse migration covers the delete-first fallback (data survives, credentials rotate)"`

---

### Task 8: Full verification

- [ ] **Step 1:** `go build ./... && go test -count=1 ./controllers/operator/ ./api/...` — expected: PASS
- [ ] **Step 2:** `make precommit` — expected: all hooks pass (exit 2 with only the index-diff guard is acceptable)
- [ ] **Step 3:** `rg -n "reAdoptInternalAppDB|appDBDetachFinalizer|cleanupAppDBFinalizer" controllers/ docker/` — expected: no hits
- [ ] **Step 4:** Commit any generated-file changes — `git commit -am "chore: regenerate after reverse-migration v2"` (skip if clean)

---

## Self-review notes

- Spec coverage: gate state machine (T1), OM secret claim (T2), release step + defensive gate (T3), CR claim + keyfile strip + abort swap + detach defensive clear (T4), machinery deletion (T5), graceful e2e (T6), fallback e2e (T7). Open item 3 of the spec (`persistent: false` validation warning) is deliberately deferred — not in this plan.
- The `isReAdoptedStatefulSetPendingReshape` reshape mechanics and `ensureAppDBRoleKeyfile`/`ensureAppDBRoleUser` continuity logic from the current uncommitted work are kept as-is; only ownership handling changes.
- Live-cluster note for the executor: the user's kind cluster is mid-failed-migration; before e2e re-runs, delete the Primary OM CR, `-db` STS, its PVCs, and both shared secrets.
