# External AppDB via MongoDB/MongoDBMulti CR Reference — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement the design in `docs/superpowers/specs/2026-07-02-appdb-mongodb-cr-reference-design.md` — let Ops Manager's AppDB be provisioned and owned as an ordinary `MongoDB`/`MongoDBMulti` CR, with Fresh Start, Forward Migration, and Reverse Migration procedures.

**Architecture:** Add `spec.externalApplicationDatabaseRef{Name,Kind}` to `MongoDBOpsManagerSpec` and `spec.role: AppDB` to `DbCommonSpec` (inherited by both `MongoDB` and `MongoDBMulti`). A shared naming convention (`<om-name>-db`) lets both controllers derive the same well-known password secret name with no credential copying. The OM controller computes Primary OM's connection string directly via `BuildConnectionString` on the referenced CR rather than through a secret the AppDB CR creates.

**Tech Stack:** Go, controller-runtime, kubebuilder CRDs, existing `resourceWatcher`/`watch` package, ginkgo/testify for unit tests, pytest for e2e.

**Branch:** `maciejk/external-appdb-with-ref` (currently clean: master + design docs only, single commit).

---

## Task 0: Spike — verify connection-string builder equivalence

**Files:**
- Create: `api/mongodb/v1/om/appdb_connectionstring_spike_test.go` (temporary, deleted at the end of this task once its assertion is ported into a permanent test in Task 8)

This resolves the design doc's one Blocking open item before anything else depends on it.

- [ ] **Step 1: Write a test constructing equivalent `AppDBSpec` and `MongoDB` objects and comparing `BuildConnectionURL`/`BuildConnectionString` output**

```go
package om

import (
	"testing"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/connectionstring"
	"github.com/stretchr/testify/assert"
)

func TestBuilderEquivalence_AppDBVsGenericMongoDB(t *testing.T) {
	const (
		name      = "om-test-db"
		namespace = "ns"
		username  = "mongodb-ops-manager"
		password  = "test-password"
		members   = 3
	)

	appDB := &AppDBSpec{
		Members: members,
		ConnectionSpec: ConnectionSpec{
			SharedConnectionSpec: mdbv1.SharedConnectionSpec{},
		},
	}
	appDB.OpsManagerName = "om-test"
	appDB.Namespace = namespace

	appDBURL := appDB.BuildConnectionURL(username, password, connectionstring.SchemeMongoDB, nil)

	mdb := &mdbv1.MongoDB{}
	mdb.SetName(name)
	mdb.SetNamespace(namespace)
	mdb.Spec.Members = members
	mdb.Spec.ResourceType = mdbv1.ReplicaSet

	genericURL := mdb.BuildConnectionString(username, password, connectionstring.SchemeMongoDB, nil)

	assert.Equal(t, appDBURL, genericURL, "AppDBSpec.BuildConnectionURL and MongoDB.BuildConnectionString must produce identical output for the same replica set shape, or Procedure 2's no-restart guarantee doesn't hold")
}
```

- [ ] **Step 2: Run it**

```bash
go test ./api/mongodb/v1/om/... -run TestBuilderEquivalence_AppDBVsGenericMongoDB -v
```

- [ ] **Step 3: Resolve any mismatch found**

If the assertion fails, inspect the diff (likely candidates per the design doc: port not set explicitly by `BuildConnectionURL`, hostname/service-name suffix differences). Fix by aligning `AppDBSpec.BuildConnectionURL` and `MongoDB.BuildConnectionString`'s shared `connectionstring.Builder()` inputs (both already funnel through the same builder — the fix is almost certainly a missing/extra option passed into `Builder()` on one side, in `api/mongodb/v1/om/appdb_types.go` around the `BuildConnectionURL` method, or `api/mongodb/v1/mdb/mongodb_types.go:1779-1804`'s `MongoDBConnectionStringBuilder.BuildConnectionString`). Do not proceed to Task 4 (Naming convention) until this passes.

- [ ] **Step 4: Delete the spike test file** (its assertion is ported into `TestConnectionStringComputedDirectly` in Task 8, Step 4) and commit

```bash
rm api/mongodb/v1/om/appdb_connectionstring_spike_test.go
git add -A && git commit -m "spike: confirm AppDBSpec.BuildConnectionURL and MongoDB.BuildConnectionString equivalence"
```

---

## Task 1: Shared password-secret naming function

**Files:**
- Modify: `api/mongodb/v1/om/appdb_types.go:255-258`
- Test: `api/mongodb/v1/om/appdb_types_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestOpsManagerUserPasswordSecretName_MatchesAppDBSpecMethod(t *testing.T) {
	appDB := &AppDBSpec{}
	appDB.OpsManagerName = "my-om"

	assert.Equal(t, OpsManagerUserPasswordSecretName("my-om"), appDB.GetOpsManagerUserPasswordSecretName())
	assert.Equal(t, "my-om-om-password", OpsManagerUserPasswordSecretName("my-om"))
}
```

- [ ] **Step 2: Run to verify it fails**

```bash
go test ./api/mongodb/v1/om/... -run TestOpsManagerUserPasswordSecretName_MatchesAppDBSpecMethod -v
```
Expected: FAIL with "undefined: OpsManagerUserPasswordSecretName"

- [ ] **Step 3: Extract the free function and delegate the existing method to it**

```go
// api/mongodb/v1/om/appdb_types.go:255-258, replace:

// OpsManagerUserPasswordSecretName returns the name of the secret that stores the
// Ops Manager user's password for a given OM's AppDB, regardless of whether that
// AppDB is currently managed internally (AppDBSpec) or externally (a role: AppDB
// MongoDB/MongoDBMulti CR) — both paths must resolve to the same secret name.
func OpsManagerUserPasswordSecretName(omName string) string {
	return omName + "-om-password"
}

// GetOpsManagerUserPasswordSecretName returns the name of the secret
// that will store the Ops Manager user's password.
func (m *AppDBSpec) GetOpsManagerUserPasswordSecretName() string {
	return OpsManagerUserPasswordSecretName(m.Name())
}
```

- [ ] **Step 4: Run test to verify it passes**

```bash
go test ./api/mongodb/v1/om/... -run TestOpsManagerUserPasswordSecretName_MatchesAppDBSpecMethod -v
```
Expected: PASS

- [ ] **Step 5: Run the full `om` package test suite to confirm no regression**

```bash
go test ./api/mongodb/v1/om/... -v
```
Expected: PASS (existing callers of `GetOpsManagerUserPasswordSecretName()` are unaffected since its return value is unchanged)

- [ ] **Step 6: Commit**

```bash
git add api/mongodb/v1/om/appdb_types.go api/mongodb/v1/om/appdb_types_test.go
git commit -m "refactor: extract OpsManagerUserPasswordSecretName as a free function"
```

---

## Task 2: API types — `ExternalApplicationDatabaseRef` and `Role`

**Files:**
- Modify: `api/mongodb/v1/om/opsmanager_types.go:101-164` (add field + new type)
- Modify: `api/mongodb/v1/mdb/mongodb_types.go:387-439` (add `Role` field to `DbCommonSpec`)
- Modify: `api/mongodb/v1/om/zz_generated.deepcopy.go` (regenerated, not hand-edited)
- Modify: `config/crd/bases/mongodb.com_opsmanagers.yaml`, `config/crd/bases/mongodb.com_mongodb.yaml`, `config/crd/bases/mongodb.com_mongodbmulticluster.yaml` (regenerated)

- [ ] **Step 1: Add the new type and field to `MongoDBOpsManagerSpec`**

```go
// api/mongodb/v1/om/opsmanager_types.go — insert before the closing brace at line 164,
// immediately after the existing OpsManagerURL field:

	// ExternalApplicationDatabaseRef references a MongoDB or MongoDBMulti resource
	// to use as this Ops Manager's AppDB, instead of the internally-managed one.
	// +optional
	ExternalApplicationDatabaseRef *ExternalApplicationDatabaseRef `json:"externalApplicationDatabaseRef,omitempty"`
}

// ExternalApplicationDatabaseRef references the MongoDB/MongoDBMulti resource
// playing the AppDB role for this Ops Manager instance.
type ExternalApplicationDatabaseRef struct {
	// Name of the MongoDB or MongoDBMulti resource to use as the external AppDB.
	// Must be in the same namespace as the MongoDBOpsManager resource, and named
	// <MongoDBOpsManager name>-db.
	// +kubebuilder:validation:Required
	Name string `json:"name"`

	// Kind of the referenced resource.
	// +kubebuilder:validation:Enum=MongoDB;MongoDBMulti
	// +kubebuilder:validation:Required
	Kind string `json:"kind"`
}
```

- [ ] **Step 2: Add `Role` to `DbCommonSpec`**

```go
// api/mongodb/v1/mdb/mongodb_types.go — insert before the closing brace at line 439:

	// Role marks this resource as playing a special role for another MongoDB
	// Kubernetes resource. Currently only AppDB is supported, marking this
	// resource as the externally-managed Application Database for a
	// MongoDBOpsManager resource.
	// +kubebuilder:validation:Enum=AppDB
	// +optional
	Role string `json:"role,omitempty"`
}
```

- [ ] **Step 3: Regenerate deepcopy and CRDs**

```bash
make generate
make manifests
```

- [ ] **Step 4: Confirm the new field appears in all three regenerated CRD files**

```bash
grep -A3 "externalApplicationDatabaseRef" config/crd/bases/mongodb.com_opsmanagers.yaml
grep -A3 "^\s*role:" config/crd/bases/mongodb.com_mongodb.yaml
grep -A3 "^\s*role:" config/crd/bases/mongodb.com_mongodbmulticluster.yaml
```
Expected: each shows the new field with its enum/type constraints.

- [ ] **Step 5: Build to confirm no compile errors**

```bash
go build ./...
```

- [ ] **Step 6: Commit**

```bash
git add api/mongodb/v1/om/opsmanager_types.go api/mongodb/v1/mdb/mongodb_types.go \
  api/mongodb/v1/om/zz_generated.deepcopy.go config/crd/bases/*.yaml helm_chart/crds/*.yaml public/crds.yaml
git commit -m "feat: add externalApplicationDatabaseRef and role API fields"
```

---

## Task 3: Webhook validation for `role: AppDB`

**Files:**
- Modify: `api/mongodb/v1/mdb/mongodb_validation.go:424-441` (`CommonValidators`)
- Test: `api/mongodb/v1/mdb/mongodb_validation_test.go`

- [ ] **Step 1: Write the failing tests**

```go
func TestAppDBRoleValidation_RequiresScram(t *testing.T) {
	spec := DbCommonSpec{Role: "AppDB", Members: 3, Version: "5.0.0"}
	// no Security.Authentication set
	result := validAppDBRole(spec)
	assert.Equal(t, v1.ValidationError, result.Status)
	assert.Contains(t, result.Msg, "SCRAM")
}

func TestAppDBRoleValidation_RequiresIgnoreUnknownUsers(t *testing.T) {
	spec := DbCommonSpec{
		Role:    "AppDB",
		Members: 3,
		Version: "5.0.0",
		Security: &Security{
			Authentication: &Authentication{Enabled: true, Modes: []AuthMode{"SCRAM"}, IgnoreUnknownUsers: false},
		},
	}
	result := validAppDBRole(spec)
	assert.Equal(t, v1.ValidationError, result.Status)
	assert.Contains(t, result.Msg, "ignoreUnknownUsers")
}

func TestAppDBRoleValidation_RequiresMinThreeMembers(t *testing.T) {
	spec := DbCommonSpec{
		Role: "AppDB", Members: 2, Version: "5.0.0",
		Security: &Security{Authentication: &Authentication{Enabled: true, Modes: []AuthMode{"SCRAM"}, IgnoreUnknownUsers: true}},
	}
	result := validAppDBRole(spec)
	assert.Equal(t, v1.ValidationError, result.Status)
	assert.Contains(t, result.Msg, "members")
}

func TestAppDBRoleValidation_RequiresMinVersion(t *testing.T) {
	spec := DbCommonSpec{
		Role: "AppDB", Members: 3, Version: "3.6.0",
		Security: &Security{Authentication: &Authentication{Enabled: true, Modes: []AuthMode{"SCRAM"}, IgnoreUnknownUsers: true}},
	}
	result := validAppDBRole(spec)
	assert.Equal(t, v1.ValidationError, result.Status)
	assert.Contains(t, result.Msg, "4.0.0")
}

func TestAppDBRoleValidation_PassesWithAllRequirementsMet(t *testing.T) {
	spec := DbCommonSpec{
		Role: "AppDB", Members: 3, Version: "5.0.0",
		Security: &Security{Authentication: &Authentication{Enabled: true, Modes: []AuthMode{"SCRAM"}, IgnoreUnknownUsers: true}},
	}
	result := validAppDBRole(spec)
	assert.Equal(t, v1.ValidationSuccess, result.Status)
}

func TestAppDBRoleValidation_SkippedWhenRoleNotSet(t *testing.T) {
	spec := DbCommonSpec{Members: 1, Version: "3.6.0"} // would fail every AppDB check if role were set
	result := validAppDBRole(spec)
	assert.Equal(t, v1.ValidationSuccess, result.Status)
}
```

- [ ] **Step 2: Run to verify failure**

```bash
go test ./api/mongodb/v1/mdb/... -run TestAppDBRoleValidation -v
```
Expected: FAIL with "undefined: validAppDBRole"

- [ ] **Step 3: Implement `validAppDBRole` and register it, mirroring `validAppDBVersion`'s semver pattern (`api/mongodb/v1/om/opsmanager_validation.go:57-69`)**

```go
// api/mongodb/v1/mdb/mongodb_validation.go — add near the other validators, before CommonValidators:

const appDBMinimumVersion = "4.0.0"

// validAppDBRole enforces the same requirements internal AppDB hardcodes
// unconditionally (AppDBSpec.GetAuthOptions, CRD-level Members floor,
// validAppDBVersion) on any resource opting into spec.role: AppDB.
func validAppDBRole(d DbCommonSpec) v1.ValidationResult {
	if d.Role != "AppDB" {
		return v1.ValidationSuccess()
	}

	if d.Security == nil || d.Security.Authentication == nil || !d.Security.Authentication.Enabled ||
		!containsAuthMode(d.Security.Authentication.Modes, "SCRAM") {
		return v1.ValidationError("role: AppDB requires spec.security.authentication.enabled: true with SCRAM in modes")
	}

	if !d.Security.Authentication.IgnoreUnknownUsers {
		return v1.ValidationError("role: AppDB requires spec.security.authentication.ignoreUnknownUsers: true")
	}

	if d.Members < 3 {
		return v1.ValidationError("role: AppDB requires at least 3 members")
	}

	minVersion, _ := semver.Make(appDBMinimumVersion)
	specVersion, err := semver.Make(d.Version)
	if err != nil || specVersion.LT(minVersion) {
		return v1.ValidationError(fmt.Sprintf("role: AppDB requires MongoDB version >= %s", appDBMinimumVersion))
	}

	return v1.ValidationSuccess()
}

func containsAuthMode(modes []AuthMode, target AuthMode) bool {
	for _, m := range modes {
		if m == target {
			return true
		}
	}
	return false
}
```

Then add `validAppDBRole` to the `CommonValidators` slice at `mongodb_validation.go:424-441` — this single insertion point covers both `MongoDB` and `MongoDBMulti` validation paths, since both call `CommonValidators`.

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./api/mongodb/v1/mdb/... -run TestAppDBRoleValidation -v
```
Expected: PASS (all 6)

- [ ] **Step 5: Run the full package suite**

```bash
go test ./api/mongodb/v1/mdb/... -v
```

- [ ] **Step 6: Commit**

```bash
git add api/mongodb/v1/mdb/mongodb_validation.go api/mongodb/v1/mdb/mongodb_validation_test.go
git commit -m "feat: webhook validation for spec.role: AppDB"
```

---

## Task 4: Naming convention validation (OM controller side)

**Files:**
- Modify: `controllers/operator/mongodbopsmanager_controller.go` (new function + call site near line 444)
- Test: `controllers/operator/mongodbopsmanager_controller_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestExpectedAppDBResourceName(t *testing.T) {
	om := DefaultOpsManagerBuilder().SetName("my-om").Build()
	assert.Equal(t, "my-om-db", ExpectedAppDBResourceName(om))
}

func TestValidateExternalAppDBReference_RejectsWrongName(t *testing.T) {
	ctx := context.Background()
	om := DefaultOpsManagerBuilder().SetName("my-om").
		SetExternalApplicationDatabaseRef("wrong-name", "MongoDB").Build()
	reconciler, client, _ := defaultTestOmReconciler(ctx, t, nil, "", "", om, zap.S())

	// no MongoDB CR created at all yet
	err := reconciler.validateExternalAppDBReference(ctx, om)
	assert.ErrorContains(t, err, "expected AppDB reference name my-om-db, got wrong-name")

	_ = client // silence unused if not needed by final signature
}
```

(Exact helper names `DefaultOpsManagerBuilder`, `defaultTestOmReconciler` per the existing pattern in `mongodbopsmanager_controller_test.go:302-328` — adjust the builder call chain to match whatever setter already exists or add `SetExternalApplicationDatabaseRef` to the test builder in the same file if it doesn't exist yet.)

- [ ] **Step 2: Run to verify failure**

```bash
go test ./controllers/operator/... -run "TestExpectedAppDBResourceName|TestValidateExternalAppDBReference" -v
```

- [ ] **Step 3: Implement**

```go
// controllers/operator/mongodbopsmanager_controller.go — new functions near the top of the file:

// ExpectedAppDBResourceName returns the required name for any MongoDB/MongoDBMulti
// CR referenced by this OM's externalApplicationDatabaseRef.
func ExpectedAppDBResourceName(om *omv1.MongoDBOpsManager) string {
	return om.Name + "-db"
}

// validateExternalAppDBReference checks role, version, and naming-convention
// requirements on the referenced CR before any external-AppDB behavior proceeds.
// Covers both Fresh Start and Forward Migration — this is the single validation
// point both procedures share.
func (r *OpsManagerReconciler) validateExternalAppDBReference(ctx context.Context, om *omv1.MongoDBOpsManager) error {
	ref := om.Spec.ExternalApplicationDatabaseRef
	if ref == nil {
		return nil
	}

	expectedName := ExpectedAppDBResourceName(om)
	if ref.Name != expectedName {
		return xerrors.Errorf("expected AppDB reference name %s, got %s", expectedName, ref.Name)
	}

	role, version, err := r.fetchExternalAppDBRoleAndVersion(ctx, om, ref)
	if err != nil {
		return xerrors.Errorf("failed to fetch external AppDB reference %s/%s: %w", ref.Kind, ref.Name, err)
	}
	if role != "AppDB" {
		return xerrors.Errorf("referenced resource %s does not have spec.role: AppDB", ref.Name)
	}

	minVersion, _ := semver.Make("4.0.0")
	specVersion, err := semver.Make(version)
	if err != nil || specVersion.LT(minVersion) {
		return xerrors.Errorf("referenced resource %s must be MongoDB version >= 4.0.0, got %s", ref.Name, version)
	}

	return nil
}
```

`fetchExternalAppDBRoleAndVersion` is a small helper fetching either a `MongoDB` or `MongoDBMulti` object by `ref.Kind` and returning `(spec.Role, spec.Version, error)` — write it alongside, switching on `ref.Kind`.

- [ ] **Step 4: Wire the call into `Reconcile`, immediately before the existing `SetupCommonWatchers` call at `:444`**

```go
// controllers/operator/mongodbopsmanager_controller.go:444, insert before:
if err := r.validateExternalAppDBReference(ctx, opsManager); err != nil {
    return r.updateStatus(ctx, opsManager, workflow.Failed(err), log)
}
```

- [ ] **Step 5: Run tests to verify they pass**

```bash
go test ./controllers/operator/... -run "TestExpectedAppDBResourceName|TestValidateExternalAppDBReference" -v
```

- [ ] **Step 6: Run the full controller test suite**

```bash
go test ./controllers/operator/... -run TestOpsManagerReconciler -v
```

- [ ] **Step 7: Commit**

```bash
git add controllers/operator/mongodbopsmanager_controller.go controllers/operator/mongodbopsmanager_controller_test.go
git commit -m "feat: validate externalApplicationDatabaseRef naming convention, role, and version"
```

---

## Task 5: Skip internal AppDB reconciliation and watchers when external ref is set

**Files:**
- Modify: `controllers/operator/mongodbopsmanager_controller.go:444,453` (gate both calls)
- Test: `controllers/operator/mongodbopsmanager_controller_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestReconcile_SkipsInternalAppDBWhenExternalRefSet(t *testing.T) {
	ctx := context.Background()
	om := DefaultOpsManagerBuilder().SetName("my-om").
		SetExternalApplicationDatabaseRef("my-om-db", "MongoDB").Build()
	mdb := DefaultMongoDBBuilder().SetName("my-om-db").SetRole("AppDB").Build()

	reconciler, client, _ := defaultTestOmReconciler(ctx, t, nil, "", "", om, zap.S())
	_ = client.Create(ctx, mdb)

	_, _ = reconciler.Reconcile(ctx, requestFromObject(om))

	// internal AppDB StatefulSet must never be created
	sts := appsv1.StatefulSet{}
	err := client.Get(ctx, kube.ObjectKey(om.Namespace, om.Spec.AppDB.Name()), &sts)
	assert.True(t, apiErrors.IsNotFound(err))
}
```

- [ ] **Step 2: Run to verify failure**

```bash
go test ./controllers/operator/... -run TestReconcile_SkipsInternalAppDBWhenExternalRefSet -v
```

- [ ] **Step 3: Guard both call sites**

```go
// controllers/operator/mongodbopsmanager_controller.go:444, change:
r.SetupCommonWatchers(&appDBReplicaSet, nil, nil, appDBReplicaSet.GetName())
// to:
if opsManager.Spec.ExternalApplicationDatabaseRef == nil {
    r.SetupCommonWatchers(&appDBReplicaSet, nil, nil, appDBReplicaSet.GetName())
}
```

```go
// controllers/operator/mongodbopsmanager_controller.go:453, change:
if err := appDbReconciler.ReconcileAppDB(ctx, opsManager); err != nil { ... }
// to:
if opsManager.Spec.ExternalApplicationDatabaseRef == nil {
    if err := appDbReconciler.ReconcileAppDB(ctx, opsManager); err != nil { ... }
}
```

(Preserve the existing error-handling body inside each `if`; only the guard condition is new.)

- [ ] **Step 4: Run test to verify it passes**

```bash
go test ./controllers/operator/... -run TestReconcile_SkipsInternalAppDBWhenExternalRefSet -v
```

- [ ] **Step 5: Run full OM controller suite to confirm internal-AppDB-only tests still pass unchanged**

```bash
go test ./controllers/operator/... -run TestOpsManagerReconciler -v
```

- [ ] **Step 6: Commit**

```bash
git add controllers/operator/mongodbopsmanager_controller.go controllers/operator/mongodbopsmanager_controller_test.go
git commit -m "feat: skip internal AppDB reconciliation and watchers when externalApplicationDatabaseRef is set"
```

---

## Task 6: Shared user-ensure logic on the generic MongoDB controller

**Files:**
- Modify: `controllers/operator/mongodbreplicaset_controller.go` (new function, called from `ReplicaSetReconcilerHelper.Reconcile:185`)
- Reference: `controllers/operator/appdbreplicaset_controller.go:1595` (`ensureAppDbPassword`), `api/mongodb/v1/om/appdb_types.go:200-236` (`GetAuthUsers`)
- Test: `controllers/operator/mongodbreplicaset_controller_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestEnsureAppDBRoleUser_CreatesSharedPasswordSecret(t *testing.T) {
	ctx := context.Background()
	mdb := DefaultReplicaSetBuilder().SetName("my-om-db").SetRole("AppDB").Build()
	reconciler, client, _ := defaultReplicaSetReconciler(ctx, nil, "", "", mdb)

	err := reconciler.ensureAppDBRoleUser(ctx, mdb)
	assert.NoError(t, err)

	secret := corev1.Secret{}
	err = client.Get(ctx, kube.ObjectKey(mdb.Namespace, om.OpsManagerUserPasswordSecretName("my-om-db")), &secret)
	assert.NoError(t, err)
	assert.NotEmpty(t, secret.Data["password"])
}

func TestEnsureAppDBRoleUser_ReusesExistingPassword(t *testing.T) {
	ctx := context.Background()
	mdb := DefaultReplicaSetBuilder().SetName("my-om-db").SetRole("AppDB").Build()
	reconciler, client, _ := defaultReplicaSetReconciler(ctx, nil, "", "", mdb)

	existing := secret.Builder().
		SetName(om.OpsManagerUserPasswordSecretName("my-om-db")).
		SetNamespace(mdb.Namespace).
		SetField("password", "pre-existing-password").
		Build()
	_ = client.Create(ctx, &existing)

	err := reconciler.ensureAppDBRoleUser(ctx, mdb)
	assert.NoError(t, err)

	result := corev1.Secret{}
	_ = client.Get(ctx, kube.ObjectKey(mdb.Namespace, om.OpsManagerUserPasswordSecretName("my-om-db")), &result)
	assert.Equal(t, "pre-existing-password", string(result.Data["password"]))
}
```

- [ ] **Step 2: Run to verify failure**

```bash
go test ./controllers/operator/... -run TestEnsureAppDBRoleUser -v
```

- [ ] **Step 3: Implement, computing the password secret name from the CR's own name directly (per the Naming convention section's correction — no suffix-stripping needed, since `mdb.Name` is already required to equal `<om-name>-db`, exactly matching `AppDBSpec.Name()`'s value for internal AppDB) and mirroring `ensureAppDbPassword` (`appdbreplicaset_controller.go:1595`) + `GetAuthUsers` (`appdb_types.go:200-236`)**

```go
// controllers/operator/mongodbreplicaset_controller.go — new function:

// ensureAppDBRoleUser mirrors AppDB reconciler's ensureAppDbPassword +
// AppDBSpec.GetAuthUsers, relocated here for role: AppDB CRs so both internal
// and external AppDB share the exact same password secret and user shape.
// The secret name is computed from this CR's own name directly (mdb.Name is
// already required to equal <om-name>-db by the naming convention, the exact
// same value AppDBSpec.Name() produces for internal AppDB) — no suffix-
// stripping or OM-name derivation needed.
func (r *ReplicaSetReconcilerHelper) ensureAppDBRoleUser(ctx context.Context, mdb *mdbv1.MongoDB, conn om.Connection) error {
	if mdb.Spec.Role != "AppDB" {
		return nil
	}

	secretName := om.OpsManagerUserPasswordSecretName(mdb.Name)

	existing := corev1.Secret{}
	err := r.client.Get(ctx, kube.ObjectKey(mdb.Namespace, secretName), &existing)
	if err != nil && !apiErrors.IsNotFound(err) {
		return xerrors.Errorf("failed to check for existing password secret: %w", err)
	}

	password := string(existing.Data["password"])
	if password == "" {
		password, err = generate.RandomFixedLengthStringOfSize(20)
		if err != nil {
			return xerrors.Errorf("failed to generate password: %w", err)
		}
		newSecret := secret.Builder().
			SetName(secretName).
			SetNamespace(mdb.Namespace).
			SetField("password", password).
			Build()
		if err := secrets.CreateOrUpdate(r.client, newSecret); err != nil {
			return xerrors.Errorf("failed to create password secret: %w", err)
		}
	}

	// Inject the synthetic mongodb-ops-manager user directly into OM's automation
	// config via the same read-modify-write mechanism MongoDBUserReconciler already
	// uses (handleScramShaUser, mongodbuser_controller.go:369) — NOT a local field
	// merged into some automation-config-builder step, since the generic MongoDB
	// controller has no such local builder; it pushes auth to OM via the OM
	// connection. ac.Auth.EnsureUser (controllers/om/automation_config.go:342) is
	// idempotent, safe to call every reconcile.
	omUser := om.MongoDBUser{
		Username: util.OpsManagerMongoDBUserName,
		Database: util.DefaultUserDatabase,
		Password: password,
		Roles: []*om.Role{
			{Role: "readWriteAnyDatabase", Database: "admin"},
			{Role: "dbAdminAnyDatabase", Database: "admin"},
			{Role: "clusterMonitor", Database: "admin"},
			{Role: "backup", Database: "admin"},
			{Role: "restore", Database: "admin"},
			{Role: "hostManager", Database: "admin"},
		},
	}
	// (Confirm the exact om.MongoDBUser/om.Role field names and password-hashing
	// convention directly against toOmUser, mongodbuser_controller.go:347, before
	// writing this — do not assume the shape above is byte-exact.)
	return conn.ReadUpdateAutomationConfig(func(ac *om.AutomationConfig) error {
		ac.Auth.EnsureUser(omUser)
		return nil
	}, log)
}
```

- [ ] **Step 4: Call `ensureAppDBRoleUser` from `ReplicaSetReconcilerHelper.Reconcile`, before StatefulSet creation — find the exact current call site by locating where `conn` (the `om.Connection` this reconcile loop already holds, used by `updateOmDeploymentRs`) becomes available, since `ensureAppDBRoleUser` needs it**

```go
// controllers/operator/mongodbreplicaset_controller.go, near the top of Reconcile,
// after conn is obtained:
if err := r.ensureAppDBRoleUser(ctx, r.resource, conn); err != nil {
    return r.updateStatus(ctx, r.resource, workflow.Failed(err), log)
}
```

- [ ] **Step 5: Run tests to verify they pass**

```bash
go test ./controllers/operator/... -run TestEnsureAppDBRoleUser -v
```

- [ ] **Step 6: Run full replica set controller suite**

```bash
go test ./controllers/operator/... -run TestReplicaSetReconciler -v
```

- [ ] **Step 7: Commit**

```bash
git add controllers/operator/mongodbreplicaset_controller.go controllers/operator/mongodbreplicaset_controller_test.go
git commit -m "feat: shared mongodb-ops-manager user/password logic for role: AppDB CRs"
```

---

## Task 7: Direct connection-string computation and dynamic watch (OM controller)

**Files:**
- Modify: `controllers/operator/mongodbopsmanager_controller.go` (new function + call site replacing the internal-only path at `:470`)
- Reference: `controllers/operator/mongodbopsmanager_controller.go:1073` (`watchMongoDBResourcesReferencedByKmip`, watch precedent), `api/mongodb/v1/mdb/mongodb_types.go:1681-1684` (`BuildConnectionString`)
- Test: `controllers/operator/mongodbopsmanager_controller_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestComputeExternalAppDBConnectionString_WritesFixedSecret(t *testing.T) {
	ctx := context.Background()
	om := DefaultOpsManagerBuilder().SetName("my-om").
		SetExternalApplicationDatabaseRef("my-om-db", "MongoDB").Build()
	mdb := DefaultMongoDBBuilder().SetName("my-om-db").SetRole("AppDB").SetMembers(3).Build()
	reconciler, client, _ := defaultTestOmReconciler(ctx, t, nil, "", "", om, zap.S())
	_ = client.Create(ctx, mdb)
	_ = client.Create(ctx, secret.Builder().
		SetName(om2.OpsManagerUserPasswordSecretName("my-om-db")).
		SetNamespace(om.Namespace).
		SetField("password", "test-password").
		Build())

	err := reconciler.computeExternalAppDBConnectionString(ctx, om)
	assert.NoError(t, err)

	result := corev1.Secret{}
	err = client.Get(ctx, kube.ObjectKey(om.Namespace, om.Spec.AppDB.ConnectionStringSecretName()), &result)
	assert.NoError(t, err)
	assert.Contains(t, string(result.Data["connectionString"]), "mongodb-ops-manager")
}
```

(Adjust `om.Spec.AppDB.ConnectionStringSecretName()` to whatever the existing fixed-secret naming accessor actually is — find it alongside `ensureAppDBConnectionStringInMemberCluster`, `mongodbopsmanager_controller.go:873`.)

- [ ] **Step 2: Run to verify failure**

```bash
go test ./controllers/operator/... -run TestComputeExternalAppDBConnectionString -v
```

- [ ] **Step 3: Implement, calling `BuildConnectionString` directly on the fetched CR**

```go
// controllers/operator/mongodbopsmanager_controller.go — new function:

// computeExternalAppDBConnectionString fetches the referenced MongoDB/MongoDBMulti
// CR and computes Primary OM's connection string directly via BuildConnectionString,
// writing it into the same fixed secret internal AppDB uses. No connection-string
// secret is ever created by the referenced CR itself.
func (r *OpsManagerReconciler) computeExternalAppDBConnectionString(ctx context.Context, om *omv1.MongoDBOpsManager) error {
	ref := om.Spec.ExternalApplicationDatabaseRef
	if ref == nil {
		return nil
	}

	// ref.Name is already required (validated by validateExternalAppDBReference) to
	// equal <om-name>-db, the exact same value AppDBSpec.Name() produces for internal
	// AppDB — no suffix-stripping or OM-name derivation needed here either.
	passwordSecret := corev1.Secret{}
	if err := r.client.Get(ctx, kube.ObjectKey(om.Namespace, omv1.OpsManagerUserPasswordSecretName(ref.Name)), &passwordSecret); err != nil {
		return xerrors.Errorf("failed to read shared password secret: %w", err)
	}
	password := string(passwordSecret.Data["password"])

	var connString string
	switch ref.Kind {
	case "MongoDB":
		mdb := mdbv1.MongoDB{}
		if err := r.client.Get(ctx, kube.ObjectKey(om.Namespace, ref.Name), &mdb); err != nil {
			return xerrors.Errorf("failed to fetch referenced MongoDB %s: %w", ref.Name, err)
		}
		connString = mdb.BuildConnectionString(util.OpsManagerMongoDBUserName, password, connectionstring.SchemeMongoDB, nil)
	case "MongoDBMultiCluster": // Kind's enum value matches the real CRD kind, not the shorthand "MongoDBMulti"
		mdbm := mdbmultiv1.MongoDBMultiCluster{}
		if err := r.client.Get(ctx, kube.ObjectKey(om.Namespace, ref.Name), &mdbm); err != nil {
			return xerrors.Errorf("failed to fetch referenced MongoDBMultiCluster %s: %w", ref.Name, err)
		}
		connString = mdbm.BuildConnectionString(util.OpsManagerMongoDBUserName, password, connectionstring.SchemeMongoDB, nil)
	}

	return r.ensureAppDBConnectionStringInMemberCluster(ctx, om, connString) // existing fixed-secret writer, appdbreplicaset_controller.go:873
}

// watchExternalAppDBReference establishes a dynamic watch on the referenced CR,
// mirroring the existing precedent in watchMongoDBResourcesReferencedByKmip
// (mongodbopsmanager_controller.go:1073) — not a new mechanism, same call
// pointed at a different name.
func (r *OpsManagerReconciler) watchExternalAppDBReference(om *omv1.MongoDBOpsManager) {
	ref := om.Spec.ExternalApplicationDatabaseRef
	if ref == nil {
		return
	}
	watchType := watch.MongoDB // both MongoDB and MongoDBMulti route through the same watch.MongoDB type today
	r.resourceWatcher.AddWatchedResourceIfNotAdded(ref.Name, om.Namespace, watchType, kube.ObjectKeyFromApiObject(om))
}
```

- [ ] **Step 4: Wire both into `Reconcile`, replacing the internal-only branch at `:470`**

**Important — this step also closes a gap Task 5's reviewer found**: Task 5 only guarded `SetupCommonWatchers`/`ReconcileAppDB`/the TLS/backup watch registration; it explicitly left `appDBPassword, err := appDbReconciler.ensureAppDbPassword(...)` (`:465`, just before the block this step replaces) running unconditionally, since nothing consumed its result yet when external ref was set. Now that this step adds a real consumer of the connection string (which needs the *shared* password, not the *internal* one), that unconditional internal-only call must also be guarded here — read the current code around `:465` first to see its exact current form before writing the branch below.

```go
// controllers/operator/mongodbopsmanager_controller.go, replacing the internal-only
// block that starts with `appDBPassword, err := appDbReconciler.ensureAppDbPassword(...)`
// (:465) through the appDBConnectionString/ensureAppDBConnectionStringInMemberCluster
// calls that follow it (:470ish):
if opsManager.Spec.ExternalApplicationDatabaseRef != nil {
    r.watchExternalAppDBReference(opsManager)
    if err := r.computeExternalAppDBConnectionString(ctx, opsManager); err != nil {
        return r.updateStatus(ctx, opsManager, workflow.Failed(err), log)
    }
} else {
    // existing internal-AppDB logic, unchanged: ensureAppDbPassword, then
    // buildMongoConnectionUrl, then ensureAppDBConnectionStringInMemberCluster
}
```

- [ ] **Step 4a: Add a regression test confirming the internal password secret is never created when external ref is set**

```go
func TestReconcile_ExternalAppDBRef_NeverCreatesInternalPasswordSecret(t *testing.T) {
	ctx := context.Background()
	om := DefaultOpsManagerBuilder().SetName("my-om").
		SetExternalApplicationDatabaseRef("my-om-db", "MongoDB").Build()
	mdb := DefaultMongoDBBuilder().SetName("my-om-db").SetRole("AppDB").SetMembers(3).Build()
	reconciler, client, _ := defaultTestOmReconciler(ctx, t, nil, "", "", om, zap.S())
	_ = client.Create(ctx, mdb)
	_ = client.Create(ctx, secret.Builder().
		SetName(om2.OpsManagerUserPasswordSecretName("my-om-db")).
		SetNamespace(om.Namespace).
		SetField("password", "test-password").
		Build())

	_, _ = reconciler.Reconcile(ctx, requestFromObject(om))

	// the OLD internal-AppDB naming convention (AppDBSpec.Name()-based, which for
	// this OM would be "my-om-db" too since AppDBSpec.Name() = OpsManagerName+"-db" —
	// confirm no *second*, differently-computed secret was created; this test's real
	// purpose is confirming ensureAppDbPassword's internal call path never ran, which
	// you can check more directly by asserting on a call counter/spy if the existing
	// test helpers support it, or by asserting the secret's value is unchanged from
	// what was pre-seeded above (proving nothing re-generated/overwrote it).
	result := corev1.Secret{}
	_ = client.Get(ctx, kube.ObjectKey(om.Namespace, om2.OpsManagerUserPasswordSecretName("my-om-db")), &result)
	assert.Equal(t, "test-password", string(result.Data["password"]))
}
```

- [ ] **Step 5: Run tests to verify they pass**

```bash
go test ./controllers/operator/... -run TestComputeExternalAppDBConnectionString -v
```

- [ ] **Step 6: Run full OM controller suite**

```bash
go test ./controllers/operator/... -run TestOpsManagerReconciler -v
```

- [ ] **Step 7: Commit**

```bash
git add controllers/operator/mongodbopsmanager_controller.go controllers/operator/mongodbopsmanager_controller_test.go
git commit -m "feat: compute external AppDB connection string directly via BuildConnectionString, with dynamic watch"
```

This completes **Procedure 1 (Fresh Start)** end to end: Task 2 (API) + Task 3 (webhook) + Task 6 (user/password) + Task 4+7 (OM-side validation, computation, watch).

---

## Task 8: Forward Migration — OM controller detach sequence

**Files:**
- Modify: `controllers/operator/mongodbopsmanager_controller.go` (new detach function)
- Test: `controllers/operator/mongodbopsmanager_controller_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestDetachInternalAppDB_StripsOwnerReferencesAndAnnotates(t *testing.T) {
	ctx := context.Background()
	om := DefaultOpsManagerBuilder().SetName("my-om").
		SetExternalApplicationDatabaseRef("my-om-db", "MongoDB").Build()
	sts := DefaultStatefulSetBuilder().SetName("my-om-db").
		SetOwnerReferences(kube.BaseOwnerReference(om)).Build()
	mdb := DefaultMongoDBBuilder().SetName("my-om-db").SetRole("AppDB").SetVersion("5.0.0").Build()

	reconciler, client, _ := defaultTestOmReconciler(ctx, t, nil, "", "", om, zap.S())
	_ = client.Create(ctx, &sts)
	_ = client.Create(ctx, mdb)

	err := reconciler.detachInternalAppDB(ctx, om)
	assert.NoError(t, err)

	result := appsv1.StatefulSet{}
	_ = client.Get(ctx, kube.ObjectKey(om.Namespace, "my-om-db"), &result)
	assert.Empty(t, result.OwnerReferences)
	assert.Equal(t, "true", result.Annotations["mongodb.com/appdb-migration-ready"])
}
```

- [ ] **Step 2: Run to verify failure**

```bash
go test ./controllers/operator/... -run TestDetachInternalAppDB -v
```

- [ ] **Step 3: Implement**

```go
// controllers/operator/mongodbopsmanager_controller.go — new function:

const appDBMigrationReadyAnnotation = "mongodb.com/appdb-migration-ready"

// detachInternalAppDB performs the one-time forward-migration detach: validate,
// strip OwnerReferences, annotate ready. Idempotent — safe to call every reconcile
// while externalApplicationDatabaseRef is set and detach hasn't completed yet.
func (r *OpsManagerReconciler) detachInternalAppDB(ctx context.Context, om *omv1.MongoDBOpsManager) error {
	if err := r.validateExternalAppDBReference(ctx, om); err != nil {
		return err
	}

	sts := appsv1.StatefulSet{}
	stsKey := kube.ObjectKey(om.Namespace, om.Spec.ExternalApplicationDatabaseRef.Name)
	if err := r.client.Get(ctx, stsKey, &sts); err != nil {
		if apiErrors.IsNotFound(err) {
			return nil // Fresh Start, nothing to detach
		}
		return xerrors.Errorf("failed to fetch StatefulSet %s: %w", stsKey.Name, err)
	}

	sts.OwnerReferences = nil
	if sts.Annotations == nil {
		sts.Annotations = map[string]string{}
	}
	sts.Annotations[appDBMigrationReadyAnnotation] = "true"
	if err := r.client.Update(ctx, &sts); err != nil {
		return xerrors.Errorf("failed to strip OwnerReferences and annotate StatefulSet: %w", err)
	}

	// Also strip OwnerReferences from the password secret and ConfigMaps this
	// OM previously owned for internal AppDB — same pattern, same client.Update.
	return r.stripInternalAppDBOwnerReferencesFromSecretsAndConfigMaps(ctx, om)
}
```

- [ ] **Step 4: Wire into `Reconcile`, immediately after `validateExternalAppDBReference` (Task 4, Step 4)**

```go
if opsManager.Spec.ExternalApplicationDatabaseRef != nil {
    if err := r.detachInternalAppDB(ctx, opsManager); err != nil {
        return r.updateStatus(ctx, opsManager, workflow.Failed(err), log)
    }
}
```

- [ ] **Step 5: Run tests to verify they pass**

```bash
go test ./controllers/operator/... -run TestDetachInternalAppDB -v
```

- [ ] **Step 6: Commit**

```bash
git add controllers/operator/mongodbopsmanager_controller.go controllers/operator/mongodbopsmanager_controller_test.go
git commit -m "feat: forward-migration detach sequence (strip OwnerReferences, annotate ready)"
```

---

## Task 9: Forward Migration — two-signal adoption gate (MongoDB controller)

**Files:**
- Modify: `controllers/operator/mongodbreplicaset_controller.go:185` (gate before StatefulSet patch)
- Test: `controllers/operator/mongodbreplicaset_controller_test.go`

- [ ] **Step 1: Write the failing tests**

```go
func TestAdoptionGate_BlocksWithoutAnnotation(t *testing.T) {
	ctx := context.Background()
	sts := DefaultStatefulSetBuilder().SetName("my-om-db").
		SetOwnerReferences(someOtherOwnerReference()).Build() // foreign STS, no annotation
	mdb := DefaultReplicaSetBuilder().SetName("my-om-db").SetRole("AppDB").Build()
	reconciler, client, _ := defaultReplicaSetReconciler(ctx, nil, "", "", mdb)
	_ = client.Create(ctx, &sts)

	blocked, err := reconciler.checkAdoptionGate(ctx, mdb)
	assert.NoError(t, err)
	assert.True(t, blocked)
}

func TestAdoptionGate_BlocksWithAnnotationButOwnerRefStillPresent(t *testing.T) {
	ctx := context.Background()
	sts := DefaultStatefulSetBuilder().SetName("my-om-db").
		SetOwnerReferences(someOtherOwnerReference()).
		SetAnnotations(map[string]string{appDBMigrationReadyAnnotation: "true"}).Build()
	mdb := DefaultReplicaSetBuilder().SetName("my-om-db").SetRole("AppDB").Build()
	reconciler, client, _ := defaultReplicaSetReconciler(ctx, nil, "", "", mdb)
	_ = client.Create(ctx, &sts)

	blocked, err := reconciler.checkAdoptionGate(ctx, mdb)
	assert.NoError(t, err)
	assert.True(t, blocked, "must stay blocked while the foreign OwnerReference is still present, even with the annotation")
}

func TestAdoptionGate_ProceedsWhenBothSignalsSatisfied(t *testing.T) {
	ctx := context.Background()
	sts := DefaultStatefulSetBuilder().SetName("my-om-db").
		SetOwnerReferences(nil).
		SetAnnotations(map[string]string{appDBMigrationReadyAnnotation: "true"}).Build()
	mdb := DefaultReplicaSetBuilder().SetName("my-om-db").SetRole("AppDB").Build()
	reconciler, client, _ := defaultReplicaSetReconciler(ctx, nil, "", "", mdb)
	_ = client.Create(ctx, &sts)

	blocked, err := reconciler.checkAdoptionGate(ctx, mdb)
	assert.NoError(t, err)
	assert.False(t, blocked)
}

func TestAdoptionGate_NoGateWhenNoExistingStatefulSet(t *testing.T) {
	ctx := context.Background()
	mdb := DefaultReplicaSetBuilder().SetName("fresh-start-db").SetRole("AppDB").Build()
	reconciler, _, _ := defaultReplicaSetReconciler(ctx, nil, "", "", mdb)

	blocked, err := reconciler.checkAdoptionGate(ctx, mdb)
	assert.NoError(t, err)
	assert.False(t, blocked, "Fresh Start: no StatefulSet exists yet, no adoption gate applies")
}
```

- [ ] **Step 2: Run to verify failure**

```bash
go test ./controllers/operator/... -run TestAdoptionGate -v
```

- [ ] **Step 3: Implement**

```go
// controllers/operator/mongodbreplicaset_controller.go — new function:

// checkAdoptionGate implements the two-signal takeover gate: blocks adoption
// unless the readiness annotation is present AND the foreign OwnerReference
// is gone. Returns (blocked, error). Re-evaluated fresh every reconcile.
func (r *ReplicaSetReconcilerHelper) checkAdoptionGate(ctx context.Context, mdb *mdbv1.MongoDB) (bool, error) {
	sts := appsv1.StatefulSet{}
	err := r.client.Get(ctx, kube.ObjectKey(mdb.Namespace, mdb.Name), &sts)
	if apiErrors.IsNotFound(err) {
		return false, nil // Fresh Start
	}
	if err != nil {
		return false, xerrors.Errorf("failed to check existing StatefulSet: %w", err)
	}

	ownsIt := false
	for _, ref := range sts.OwnerReferences {
		if ref.UID == mdb.UID {
			ownsIt = true
		}
	}
	if ownsIt {
		return false, nil // already adopted, not a foreign StatefulSet
	}

	annotationReady := sts.Annotations[appDBMigrationReadyAnnotation] == "true"
	foreignOwnerRefGone := len(sts.OwnerReferences) == 0

	return !(annotationReady && foreignOwnerRefGone), nil
}
```

- [ ] **Step 4: Wire into `Reconcile` (`:185`), before StatefulSet creation/patch, reporting a waiting status when blocked**

```go
if mdb.Spec.Role == "AppDB" {
    blocked, err := r.checkAdoptionGate(ctx, mdb)
    if err != nil {
        return r.updateStatus(ctx, mdb, workflow.Failed(err), log)
    }
    if blocked {
        return r.updateStatus(ctx, mdb, workflow.Pending("waiting for Ops Manager to finish detaching AppDB StatefulSet %s", mdb.Name), log)
    }
    // clear the annotation once adopted — best-effort, see design doc's Idempotency section
}
```

- [ ] **Step 5: Run tests to verify they pass**

```bash
go test ./controllers/operator/... -run TestAdoptionGate -v
```

- [ ] **Step 6: Run full replica set controller suite**

```bash
go test ./controllers/operator/... -run TestReplicaSetReconciler -v
```

- [ ] **Step 7: Commit**

```bash
git add controllers/operator/mongodbreplicaset_controller.go controllers/operator/mongodbreplicaset_controller_test.go
git commit -m "feat: two-signal adoption gate for AppDB StatefulSet takeover"
```

This completes **Procedure 2 (Forward Migration)**: Task 8 (OM detach) + Task 9 (MongoDB adoption gate), reusing Task 6's shared-user logic and Task 7's connection-string computation (already branch on `ExternalApplicationDatabaseRef != nil` regardless of fresh-start vs. migration).

---

## Task 10: Reverse Migration — finalizer registration and cleanup

**Files:**
- Modify: `controllers/operator/mongodbreplicaset_controller.go` (finalizer add/cleanup, mirroring `mongodbuser_controller.go:540` `ensureFinalizer` and its call site at `:225`)
- Test: `controllers/operator/mongodbreplicaset_controller_test.go`

- [ ] **Step 1: Write the failing tests**

```go
func TestAppDBFinalizer_AddedWhenRoleSet(t *testing.T) {
	ctx := context.Background()
	mdb := DefaultReplicaSetBuilder().SetName("my-om-db").SetRole("AppDB").Build()
	reconciler, client, _ := defaultReplicaSetReconciler(ctx, nil, "", "", mdb)

	err := reconciler.ensureAppDBFinalizer(ctx, mdb)
	assert.NoError(t, err)

	result := mdbv1.MongoDB{}
	_ = client.Get(ctx, kube.ObjectKey(mdb.Namespace, mdb.Name), &result)
	assert.Contains(t, result.Finalizers, "mongodb.com/appdb-detach")
}

func TestAppDBFinalizer_NotAddedForOrdinaryReplicaSet(t *testing.T) {
	ctx := context.Background()
	mdb := DefaultReplicaSetBuilder().SetName("ordinary-rs").Build() // no role set
	reconciler, client, _ := defaultReplicaSetReconciler(ctx, nil, "", "", mdb)

	err := reconciler.ensureAppDBFinalizer(ctx, mdb)
	assert.NoError(t, err)

	result := mdbv1.MongoDB{}
	_ = client.Get(ctx, kube.ObjectKey(mdb.Namespace, mdb.Name), &result)
	assert.NotContains(t, result.Finalizers, "mongodb.com/appdb-detach")
}

func TestAppDBFinalizerCleanup_StripsOwnerReferenceAndAnnotates(t *testing.T) {
	ctx := context.Background()
	mdb := DefaultReplicaSetBuilder().SetName("my-om-db").SetRole("AppDB").
		SetFinalizers([]string{"mongodb.com/appdb-detach"}).
		SetDeletionTimestamp(metav1.Now()).Build()
	sts := DefaultStatefulSetBuilder().SetName("my-om-db").
		SetOwnerReferences(kube.BaseOwnerReference(mdb)).Build()
	reconciler, client, _ := defaultReplicaSetReconciler(ctx, nil, "", "", mdb)
	_ = client.Create(ctx, &sts)

	err := reconciler.cleanupAppDBFinalizer(ctx, mdb)
	assert.NoError(t, err)

	result := appsv1.StatefulSet{}
	_ = client.Get(ctx, kube.ObjectKey(mdb.Namespace, "my-om-db"), &result)
	assert.Empty(t, result.OwnerReferences)
	assert.Equal(t, "true", result.Annotations[appDBMigrationReadyAnnotation])

	updatedMdb := mdbv1.MongoDB{}
	err = client.Get(ctx, kube.ObjectKey(mdb.Namespace, mdb.Name), &updatedMdb)
	assert.True(t, apiErrors.IsNotFound(err), "finalizer removal should let deletion complete")
}
```

- [ ] **Step 2: Run to verify failure**

```bash
go test ./controllers/operator/... -run "TestAppDBFinalizer" -v
```

- [ ] **Step 3: Implement, mirroring `MongoDBUserReconciler.ensureFinalizer` (`mongodbuser_controller.go:540`)**

```go
// controllers/operator/mongodbreplicaset_controller.go — new functions:

const appDBDetachFinalizer = "mongodb.com/appdb-detach"

func (r *ReplicaSetReconcilerHelper) ensureAppDBFinalizer(ctx context.Context, mdb *mdbv1.MongoDB) error {
	if mdb.Spec.Role != "AppDB" {
		return nil
	}
	if controllerutil.ContainsFinalizer(mdb, appDBDetachFinalizer) {
		return nil
	}
	controllerutil.AddFinalizer(mdb, appDBDetachFinalizer)
	return r.client.Update(ctx, mdb)
}

// cleanupAppDBFinalizer mirrors detachInternalAppDB in reverse: strip own
// OwnerReference, annotate ready, then remove the finalizer so deletion completes.
func (r *ReplicaSetReconcilerHelper) cleanupAppDBFinalizer(ctx context.Context, mdb *mdbv1.MongoDB) error {
	sts := appsv1.StatefulSet{}
	if err := r.client.Get(ctx, kube.ObjectKey(mdb.Namespace, mdb.Name), &sts); err != nil {
		return xerrors.Errorf("failed to fetch StatefulSet during finalizer cleanup: %w", err)
	}

	sts.OwnerReferences = nil
	if sts.Annotations == nil {
		sts.Annotations = map[string]string{}
	}
	sts.Annotations[appDBMigrationReadyAnnotation] = "true"
	if err := r.client.Update(ctx, &sts); err != nil {
		return xerrors.Errorf("failed to strip OwnerReference and annotate during finalizer cleanup: %w", err)
	}

	controllerutil.RemoveFinalizer(mdb, appDBDetachFinalizer)
	return r.client.Update(ctx, mdb)
}
```

- [ ] **Step 4: Wire into `Reconcile` (`:185`) — add finalizer early, check `DeletionTimestamp` for cleanup before normal reconcile logic**

```go
if !mdb.DeletionTimestamp.IsZero() && mdb.Spec.Role == "AppDB" {
    if err := r.cleanupAppDBFinalizer(ctx, mdb); err != nil {
        return r.updateStatus(ctx, mdb, workflow.Failed(err), log)
    }
    return reconcile.Result{}, nil
}
if err := r.ensureAppDBFinalizer(ctx, mdb); err != nil {
    return r.updateStatus(ctx, mdb, workflow.Failed(err), log)
}
```

- [ ] **Step 5: Run tests to verify they pass**

```bash
go test ./controllers/operator/... -run "TestAppDBFinalizer" -v
```

- [ ] **Step 6: Commit**

```bash
git add controllers/operator/mongodbreplicaset_controller.go controllers/operator/mongodbreplicaset_controller_test.go
git commit -m "feat: appdb-detach finalizer registration and cleanup for reverse migration"
```

---

## Task 11: Reverse Migration — OM controller re-adoption

**Files:**
- Modify: `controllers/operator/mongodbopsmanager_controller.go` (re-adoption function)
- Test: `controllers/operator/mongodbopsmanager_controller_test.go`

- [ ] **Step 1: Write the failing tests**

```go
func TestReAdoptInternalAppDB_BlocksWithoutAnnotation(t *testing.T) {
	ctx := context.Background()
	om := DefaultOpsManagerBuilder().SetName("my-om").Build() // ref already removed
	sts := DefaultStatefulSetBuilder().SetName("my-om-db").SetOwnerReferences(nil).Build() // no annotation yet
	reconciler, client, _ := defaultTestOmReconciler(ctx, t, nil, "", "", om, zap.S())
	_ = client.Create(ctx, &sts)

	adopted, err := reconciler.reAdoptInternalAppDB(ctx, om)
	assert.NoError(t, err)
	assert.False(t, adopted)
}

func TestReAdoptInternalAppDB_SetsOwnerReferenceWhenAnnotated(t *testing.T) {
	ctx := context.Background()
	om := DefaultOpsManagerBuilder().SetName("my-om").Build()
	sts := DefaultStatefulSetBuilder().SetName("my-om-db").
		SetOwnerReferences(nil).
		SetAnnotations(map[string]string{appDBMigrationReadyAnnotation: "true"}).Build()
	reconciler, client, _ := defaultTestOmReconciler(ctx, t, nil, "", "", om, zap.S())
	_ = client.Create(ctx, &sts)

	adopted, err := reconciler.reAdoptInternalAppDB(ctx, om)
	assert.NoError(t, err)
	assert.True(t, adopted)

	result := appsv1.StatefulSet{}
	_ = client.Get(ctx, kube.ObjectKey(om.Namespace, "my-om-db"), &result)
	assert.NotEmpty(t, result.OwnerReferences)
	assert.NotContains(t, result.Annotations, appDBMigrationReadyAnnotation)
}
```

- [ ] **Step 2: Run to verify failure**

```bash
go test ./controllers/operator/... -run TestReAdoptInternalAppDB -v
```

- [ ] **Step 3: Implement**

```go
// controllers/operator/mongodbopsmanager_controller.go — new function:

// reAdoptInternalAppDB handles Procedure 3's OM-side re-adoption: gated on the
// same readiness annotation, mirroring detachInternalAppDB in reverse.
func (r *OpsManagerReconciler) reAdoptInternalAppDB(ctx context.Context, om *omv1.MongoDBOpsManager) (bool, error) {
	stsKey := kube.ObjectKey(om.Namespace, om.Spec.AppDB.Name())
	sts := appsv1.StatefulSet{}
	if err := r.client.Get(ctx, stsKey, &sts); err != nil {
		return false, xerrors.Errorf("failed to fetch StatefulSet during re-adoption: %w", err)
	}

	if sts.Annotations[appDBMigrationReadyAnnotation] != "true" {
		return false, nil // still blocked, keep skipping ReconcileAppDB
	}

	sts.OwnerReferences = append(sts.OwnerReferences, kube.BaseOwnerReference(om))
	delete(sts.Annotations, appDBMigrationReadyAnnotation)
	if err := r.client.Update(ctx, &sts); err != nil {
		return false, xerrors.Errorf("failed to set OwnerReference and clear annotation: %w", err)
	}

	r.resourceWatcher.RemoveDependentWatchedResources(kube.ObjectKeyFromApiObject(om)) // tear down watch on the now-deleted external CR
	return true, nil
}
```

- [ ] **Step 4: Wire into `Reconcile` — when `ExternalApplicationDatabaseRef` is nil but the AppDB StatefulSet still lacks this OM's OwnerReference, attempt re-adoption before resuming `ReconcileAppDB()`/`SetupCommonWatchers`**

```go
if opsManager.Spec.ExternalApplicationDatabaseRef == nil {
    adopted, err := r.reAdoptInternalAppDBIfNeeded(ctx, opsManager) // checks whether re-adoption is even pending first
    if err != nil {
        return r.updateStatus(ctx, opsManager, workflow.Failed(err), log)
    }
    if !adopted {
        return r.updateStatus(ctx, opsManager, workflow.Pending("waiting for MongoDB controller to finish detaching AppDB StatefulSet"), log)
    }
    // fall through to existing SetupCommonWatchers/ReconcileAppDB calls, now unguarded
}
```

(`reAdoptInternalAppDBIfNeeded` is a thin wrapper checking whether the StatefulSet already carries this OM's OwnerReference — if so, returns `true` immediately without re-running the annotation check, so normal internal-AppDB reconciles after the migration completes don't pay this cost every time.)

- [ ] **Step 5: Run tests to verify they pass**

```bash
go test ./controllers/operator/... -run TestReAdoptInternalAppDB -v
```

- [ ] **Step 6: Run full OM controller suite**

```bash
go test ./controllers/operator/... -run TestOpsManagerReconciler -v
```

- [ ] **Step 7: Commit**

```bash
git add controllers/operator/mongodbopsmanager_controller.go controllers/operator/mongodbopsmanager_controller_test.go
git commit -m "feat: OM re-adoption of internal AppDB StatefulSet on reverse migration"
```

This completes **Procedure 3 (Reverse Migration)**: Task 10 (MongoDB finalizer) + Task 11 (OM re-adoption).

---

## Task 12: e2e test

**Files:**
- Create: `docker/mongodb-kubernetes-tests/tests/opsmanager/om_external_appdb.py`
- Create: `docker/mongodb-kubernetes-tests/tests/opsmanager/fixtures/om_external_appdb_meta.yaml`, `om_external_appdb_primary.yaml`, `om_external_appdb_db.yaml`
- Reference (structure/style only, per your instruction — not copied wholesale): `maciejk/external-appdb-with-connstring`'s `om_external_appdb.py` for the phase breakdown (setup, pre-switch canary, switch, takeover, backup+PIT restore) and its fixture-file conventions

- [ ] **Step 1: Write fixture YAMLs** — a `MongoDBOpsManager` fixture (`om_external_appdb_primary.yaml`) with no `externalApplicationDatabaseRef` initially, and a `MongoDB` fixture (`om_external_appdb_db.yaml`) named `<om-name>-db` with `spec.role: AppDB`, 3 members, SCRAM + `ignoreUnknownUsers: true`, version `>= 4.0.0` — matching Task 3's webhook requirements exactly.

- [ ] **Step 2: Write `TestFreshStartExternalAppDB`** — create the OM CR without the ref, create the `MongoDB` CR fixture, then set `externalApplicationDatabaseRef` on the OM CR; assert the MongoDB CR reaches `Running`, the shared password secret exists (`<om-name>-db-om-password`), and Primary OM's fixed connection-string secret contains a working URI (connect and run a command).

- [ ] **Step 3: Write `TestSentinelDocSurvivesForwardMigration`** — start with an OM CR using internal AppDB; write a sentinel document; create the `MongoDB` CR fixture named `<om-name>-db` with `spec.role: AppDB`; set `externalApplicationDatabaseRef`; wait for the adoption gate to clear and the MongoDB CR to reach `Running`; assert the sentinel document is still present, and assert `restartCount`/pod creation timestamp for Primary OM's pod — **conditional per Open Item 1**: only assert *unchanged* once Task 0's spike confirms builder equivalence; otherwise assert *at most one* restart and document why in a comment.

- [ ] **Step 4: Write `TestReverseMigrationPreservesDataAndCredentials`** — starting from a completed forward migration, delete the `MongoDB` CR and remove `externalApplicationDatabaseRef` together; assert the CR stays in `deletionTimestamp`-set state until cleanup completes; assert internal AppDB management resumes; assert the sentinel document survives; assert the password secret's value is unchanged throughout (read it before and after, compare bytes).

- [ ] **Step 5: Write `TestAdoptionGateBlocksWithoutBothSignals`** — manually create a StatefulSet named `<om-name>-db` with a foreign OwnerReference and no annotation; create the matching `MongoDB` CR; assert it reports a waiting status and never modifies the StatefulSet. Repeat with the annotation present but the OwnerReference still there — assert still blocked.

- [ ] **Step 6: Write `TestNamingConventionRejectsWrongName`** — set `externalApplicationDatabaseRef.Name` to something not matching `<om-name>-db`; assert the OM CR reports a clear validation error and never proceeds with any external-AppDB behavior.

- [ ] **Step 7: Run the full e2e suite locally against a kind cluster** (per `mck-dev:local-kind-dev` skill)

```bash
pytest -m e2e_om_external_appdb -v
```

- [ ] **Step 8: Commit**

```bash
git add docker/mongodb-kubernetes-tests/tests/opsmanager/om_external_appdb.py \
  docker/mongodb-kubernetes-tests/tests/opsmanager/fixtures/om_external_appdb_*.yaml
git commit -m "test: e2e coverage for external AppDB via MongoDB CR reference"
```

---

## Self-Review

**Spec coverage:** Task 0 covers the Blocking open item. Tasks 1-7 cover Procedure 1 (Fresh Start) plus the shared naming-convention/password/connection-string mechanisms every other procedure depends on. Tasks 8-9 cover Procedure 2 (Forward Migration). Tasks 10-11 cover Procedure 3 (Reverse Migration). Task 12 covers the Testing plan's e2e scenarios. Non-blocking Open Items 2-6 (MongoDBMulti builder verification, TLS reversal, ordering hazard, watch teardown, naming soft-coupling) are exercised implicitly by Tasks 7, 8, 9, 11's tests but not each individually spiked — acceptable per the design doc's own "non-blocking" categorization.

**Placeholder scan:** No "TBD"/"TODO" placeholders. `fetchExternalAppDBRoleAndVersion` (Task 4) and `stripInternalAppDBOwnerReferencesFromSecretsAndConfigMaps` (Task 8) are described rather than fully coded inline — both are small, mechanical helpers whose shape is fully determined by their call sites and existing sibling code; writing them out would just repeat the same `client.Get`/switch-on-Kind or `client.Update` pattern already shown twice elsewhere in this plan.

**Type consistency:** `appDBMigrationReadyAnnotation` and `appDBDetachFinalizer` constants are defined once (Tasks 8 and 10 respectively) and referenced by name in every later task that needs them — Task 9, 11 assume these constants already exist from earlier tasks, consistent with the stated build order.
