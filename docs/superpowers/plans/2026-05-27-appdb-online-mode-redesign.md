# AppDB Online Mode Redesign Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the bespoke `MetaOMRef`/`ManagedByMetaOM` PoC API with the standard MCK `ConnectionSpec` + `project.Reader` pattern, reorder reconciliation so the OM connection is established before monitoring, and move the AC push into the standard deploy path.

**Architecture:** Two PRs. PR 1 is purely structural (API types, Reader interface, rename constructs, refactor `reconcileOMConnection` to use standard pipeline). PR 2 changes runtime behaviour (reconcile order, monitoring strategy, AC push to external OM, multi-cluster agent key replication). Both PRs are buildable and testable independently.

**Tech Stack:** Go, controller-runtime, kubebuilder markers, testify, existing `project.ReadConfigAndCredentials`, `connection.PrepareOpsManagerConnection`, `agents.EnsureAgentKeySecretExists`.

**Spec:** `docs/superpowers/specs/2026-05-27-appdb-online-mode-redesign.md`

---

## File Map

| File | Change |
|---|---|
| `controllers/operator/project/project.go` | Replace `metav1.Object` with `GetName() string` in `Reader` |
| `api/v1/om/appdb_types.go` | Remove `ManagedByMetaOM`/`MetaOMRef`; add `Connection *ConnectionSpec`; add 5 Reader methods to `AppDBSpec` |
| `api/v1/om/opsmanager_types.go` | Rename `MetaOMGroupID` → `ExternalGroupID` in `AppDbStatus` |
| `controllers/operator/construct/appdb_construction.go` | Rename `MetaOMEnvVars` → `AgentConnectionConfig`; rename `AppDBStatefulSetOptions.MetaOM` → `.Connection`; update `ShouldEnableMonitoring` gate |
| `controllers/operator/appdbreplicaset_controller.go` | Rename `reconcileManagedByMetaOM` → `reconcileOMConnection`; replace its body; reorder reconcile loop; gate monitoring to headless; update `deployAutomationConfig` to push AC to external OM; add `replicateAgentKeySecret` |
| `controllers/operator/construct/appdb_construction_test.go` | Rename test field references |
| `controllers/operator/appdbreplicaset_controller_test.go` | Update tests for new function signatures and renamed fields |
| `docker/mongodb-kubernetes-tests/tests/opsmanager/om_appdb_meta_om_mode_switch.py` | Update YAML fixture field names; update agent key secret name references |
| `docker/mongodb-kubernetes-tests/tests/opsmanager/fixtures/om_appdb_switch_primary_om.yaml` | Rename `managedByMetaOM` → `connection` |
| `config/crd/bases/` + `helm_chart/crds/` + `public/crds.yaml` | Auto-generated via `make generate` |

---

## PR 1 — API + Connection Pipeline

---

### Task 1: Update `project.Reader` — replace `metav1.Object` with `GetName() string`

**Files:**
- Modify: `controllers/operator/project/project.go:24-30`

Background: `ReadConfigAndCredentials` (line 35) calls `reader.GetName()` only. Nothing else from `metav1.Object` is used. Replacing the embedded interface makes `AppDBSpec` (a plain struct) able to satisfy `Reader` without a wrapper.

- [ ] **Step 1: Verify no other metav1.Object methods are called on Reader**

```bash
grep -n "reader\." controllers/operator/project/project.go
```

Expected: only `reader.GetProjectConfigMapNamespace()`, `reader.GetProjectConfigMapName()`, `reader.GetName()`, `reader.GetCredentialsSecretNamespace()`, `reader.GetCredentialsSecretName()` appear.

- [ ] **Step 2: Update the interface**

In `controllers/operator/project/project.go`, replace lines 24-30:

```go
// Reader returns the name of a ConfigMap which contains Ops Manager project details.
// and the name of a secret containing project credentials.
type Reader interface {
	GetName() string
	GetProjectConfigMapName() string
	GetProjectConfigMapNamespace() string
	GetCredentialsSecretName() string
	GetCredentialsSecretNamespace() string
}
```

- [ ] **Step 3: Verify build still compiles**

```bash
go build ./controllers/operator/project/... 2>&1
```

Expected: no errors. Existing implementors (`MongoDB`, `MongoDBMultiCluster`, etc.) already have `GetName()` via embedded `ObjectMeta` — no changes to those files.

- [ ] **Step 4: Run project package tests**

```bash
go test ./controllers/operator/project/... -v 2>&1 | tail -20
```

Expected: all pass.

- [ ] **Step 5: Commit**

```bash
git add controllers/operator/project/project.go
git commit -m "refactor: replace metav1.Object in project.Reader with GetName() string"
```

---

### Task 2: API types — add `Connection *ConnectionSpec`, remove `MetaOMRef`, implement Reader on `AppDBSpec`

**Files:**
- Modify: `api/v1/om/appdb_types.go`

Background: `AppDBSpec.Name()` (line 413) returns `m.OpsManagerName + "-db"`. `AppDBSpec.Namespace` field exists. The inline `ConnectionSpec` field already has `GetProject()` (returns ConfigMap name) and `Credentials` (credentials secret name). The new `Connection *ConnectionSpec` pointer field uses those same accessors.

- [ ] **Step 1: Write a compile-time Reader check test**

Add to `api/v1/om/appdb_types_test.go` (create if it doesn't exist):

```go
package omv1_test

import (
	"testing"
	omv1 "github.com/mongodb/mongodb-kubernetes/api/v1/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/project"
)

// Compile-time check: AppDBSpec must satisfy project.Reader.
var _ project.Reader = &omv1.AppDBSpec{}

func TestAppDBSpec_ReaderMethods(t *testing.T) {
	spec := &omv1.AppDBSpec{
		Namespace:      "test-ns",
		OpsManagerName: "om-primary",
	}
	spec.Connection = &omv1.ConnectionSpec{
		SharedConnectionSpec: mdbv1.SharedConnectionSpec{
			OpsManagerConfig: mdbv1.OpsManagerConfig{
				ConfigMapRef: mdbv1.ConfigMapRef{Name: "my-project-config"},
			},
		},
		Credentials: "my-credentials-secret",
	}

	assert.Equal(t, "om-primary-db", spec.GetName())
	assert.Equal(t, "my-project-config", spec.GetProjectConfigMapName())
	assert.Equal(t, "test-ns", spec.GetProjectConfigMapNamespace())
	assert.Equal(t, "my-credentials-secret", spec.GetCredentialsSecretName())
	assert.Equal(t, "test-ns", spec.GetCredentialsSecretNamespace())
}
```

- [ ] **Step 2: Run to verify it fails to compile**

```bash
go test ./api/v1/om/... 2>&1 | head -20
```

Expected: compile error — `AppDBSpec` does not implement `project.Reader`.

- [ ] **Step 3: Remove `ManagedByMetaOM` field and `MetaOMRef` struct**

In `api/v1/om/appdb_types.go`:
- Delete the `ManagedByMetaOM *MetaOMRef` field from `AppDBSpec` (currently last field, after `ClusterSpecList`)
- Delete the entire `MetaOMRef` struct definition (lines ~113-136)

- [ ] **Step 4: Add `Connection *ConnectionSpec` field to `AppDBSpec`**

Add after `ClusterSpecList` in `AppDBSpec`:

```go
// Connection, when set, switches AppDB agents from headless mode to online
// mode connected to the referenced external Ops Manager instance.
// Follows the same ConfigMap + credentials Secret pattern as any MongoDB CR.
// +optional
Connection *ConnectionSpec `json:"connection,omitempty"`
```

Note: `ConnectionSpec` is already imported — it is the same type inlined as `ConnectionSpec json:",inline"` elsewhere in the struct.

- [ ] **Step 5: Add five Reader methods to `AppDBSpec`**

Add after the `Name()` method (line ~413):

```go
// GetName implements project.Reader.
func (m *AppDBSpec) GetName() string { return m.Name() }

// GetProjectConfigMapName implements project.Reader.
func (m *AppDBSpec) GetProjectConfigMapName() string { return m.Connection.GetProject() }

// GetProjectConfigMapNamespace implements project.Reader.
func (m *AppDBSpec) GetProjectConfigMapNamespace() string { return m.Namespace }

// GetCredentialsSecretName implements project.Reader.
func (m *AppDBSpec) GetCredentialsSecretName() string { return m.Connection.Credentials }

// GetCredentialsSecretNamespace implements project.Reader.
func (m *AppDBSpec) GetCredentialsSecretNamespace() string { return m.Namespace }
```

- [ ] **Step 6: Run the test to verify it passes**

```bash
go test ./api/v1/om/... -run TestAppDBSpec_ReaderMethods -v 2>&1
```

Expected: PASS.

- [ ] **Step 7: Verify build**

```bash
go build ./... 2>&1 | head -30
```

Expected: compile errors in `appdbreplicaset_controller.go` only — references to `ManagedByMetaOM` and `MetaOMRef` that will be fixed in Task 4. Fix any immediate compilation errors in `appdb_types.go` itself.

- [ ] **Step 8: Commit**

```bash
git add api/v1/om/appdb_types.go api/v1/om/appdb_types_test.go
git commit -m "feat: add Connection *ConnectionSpec to AppDBSpec and implement project.Reader"
```

---

### Task 3: Rename status field, rename construct types

**Files:**
- Modify: `api/v1/om/opsmanager_types.go:474-481`
- Modify: `controllers/operator/construct/appdb_construction.go:57-81`

- [ ] **Step 1: Rename `MetaOMGroupID` → `ExternalGroupID` in `AppDbStatus`**

In `api/v1/om/opsmanager_types.go`, replace the `AppDbStatus` struct:

```go
type AppDbStatus struct {
	mdbv1.MongoDbStatus `json:",inline"`
	ClusterStatusList   []status.ClusterStatusItem `json:"clusterStatusList,omitempty"`
	// ExternalGroupID stores the project (group) ID assigned by the external OM
	// once the AppDB has been registered. Used for observability.
	// +optional
	ExternalGroupID string `json:"externalGroupID,omitempty"`
}
```

- [ ] **Step 2: Rename `MetaOMEnvVars` → `AgentConnectionConfig` in `appdb_construction.go`**

In `controllers/operator/construct/appdb_construction.go`, replace the struct at line ~57:

```go
// AgentConnectionConfig holds the connection parameters for switching an AppDB agent
// from headless mode to online mode.
type AgentConnectionConfig struct {
	// Enabled, when true, switches the agent from headless mode to online mode.
	// Server and GroupID must be set.
	Enabled bool
	Server  string
	GroupID string
}
```

- [ ] **Step 3: Rename `AppDBStatefulSetOptions.MetaOM` → `.Connection`**

In `AppDBStatefulSetOptions` (line ~67), update the field:

```go
type AppDBStatefulSetOptions struct {
	VaultConfig vault.VaultConfiguration
	CertHash    string

	InitAppDBImage             string
	MongodbImage               string
	AgentImage                 string
	LegacyMonitoringAgentImage string

	PrometheusTLSCertHash string

	// Connection holds OM connection env vars for online mode.
	// When Connection.Enabled is true, the StatefulSet is built in online mode.
	Connection AgentConnectionConfig
}
```

- [ ] **Step 4: Update all internal references in `appdb_construction.go`**

```bash
grep -n "MetaOM\|MetaOMEnvVars" controllers/operator/construct/appdb_construction.go
```

Replace each occurrence:
- `opts.MetaOM` → `opts.Connection`
- `MetaOMEnvVars{` → `AgentConnectionConfig{`
- Any remaining `MetaOM` prefix → `Connection` or `AgentConnection` as appropriate

- [ ] **Step 5: Run construction tests**

```bash
go test ./controllers/operator/construct/... -v 2>&1 | tail -30
```

Expected: all pass (some may fail due to renamed field references in tests — fix those too, same rename pattern).

- [ ] **Step 6: Commit**

```bash
git add api/v1/om/opsmanager_types.go \
        controllers/operator/construct/appdb_construction.go \
        controllers/operator/construct/appdb_construction_test.go
git commit -m "refactor: rename MetaOMEnvVars->AgentConnectionConfig, MetaOM->Connection, MetaOMGroupID->ExternalGroupID"
```

---

### Task 4: Refactor `reconcileManagedByMetaOM` → `reconcileOMConnection`

**Files:**
- Modify: `controllers/operator/appdbreplicaset_controller.go`

Background: `reconcileManagedByMetaOM` (line 2267) currently does manual credential reading, Meta OM CR lookup, project creation, AC push, and agent key creation. After this task, it delegates to `project.ReadConfigAndCredentials` + `connection.PrepareOpsManagerConnection`. The AC push logic stays temporarily (moved in PR 2 Task 8).

- [ ] **Step 1: Write a failing test for `reconcileOMConnection`**

Add to `controllers/operator/appdbreplicaset_controller_test.go`:

```go
func TestReconcileOMConnection_PopulatesAgentConnectionConfig(t *testing.T) {
	ctx := context.Background()
	
	// Build an OpsManager with Connection set
	om := omv1.NewOpsManagerBuilderDefault().Build()
	om.Spec.AppDB.Connection = &omv1.ConnectionSpec{
		SharedConnectionSpec: mdbv1.SharedConnectionSpec{
			OpsManagerConfig: mdbv1.OpsManagerConfig{
				ConfigMapRef: mdbv1.ConfigMapRef{Name: "appdb-project-config"},
			},
		},
		Credentials: "appdb-credentials",
	}

	// Project ConfigMap: contains OM base URL and project name
	projectCM := configmap.Builder().
		SetName("appdb-project-config").
		SetNamespace(om.Namespace).
		SetDataField("projectName", "primary-appdb").
		SetDataField("baseUrl", "http://om.local:8080").
		Build()

	// Credentials Secret
	credSecret := secret.Builder().
		SetName("appdb-credentials").
		SetNamespace(om.Namespace).
		SetField("publicKey", "pub-key").
		SetField("privateKey", "priv-key").
		Build()

	reconciler, _, _ := defaultTestOmReconciler(ctx, t, nil, "", "", om, nil, om.NewMockedConnectionFactory())
	require.NoError(t, reconciler.client.Create(ctx, projectCM))
	require.NoError(t, reconciler.client.Create(ctx, credSecret))

	conn, config, ws := reconciler.reconcileOMConnection(ctx, om, zaptest.NewLogger(t).Sugar())

	require.True(t, ws.IsOK())
	assert.NotNil(t, conn)
	assert.True(t, config.Enabled)
	assert.Equal(t, "http://om.local:8080", config.Server)
	assert.NotEmpty(t, config.GroupID)
}
```

- [ ] **Step 2: Run to verify it fails**

```bash
go test ./controllers/operator/... -run TestReconcileOMConnection_PopulatesAgentConnectionConfig -v 2>&1 | head -20
```

Expected: compile error — `reconcileOMConnection` does not exist yet.

- [ ] **Step 3: Rename the function and replace its body**

In `controllers/operator/appdbreplicaset_controller.go` at line 2267, replace the entire `reconcileManagedByMetaOM` function with:

```go
// reconcileOMConnection establishes the AppDB's connection to an external Ops Manager
// by reading the project ConfigMap and credentials Secret, then delegating to the
// standard PrepareOpsManagerConnection pipeline.
func (r *ReconcileAppDbReplicaSet) reconcileOMConnection(
	ctx context.Context,
	opsManager *omv1.MongoDBOpsManager,
	log *zap.SugaredLogger,
) (om.Connection, construct.AgentConnectionConfig, workflow.Status) {
	projectConfig, credentials, err := project.ReadConfigAndCredentials(
		ctx, r.client, r.SecretClient, &opsManager.Spec.AppDB, log)
	if err != nil {
		return nil, construct.AgentConnectionConfig{}, workflow.Failed(xerrors.Errorf("failed to read external OM config and credentials: %w", err))
	}

	conn, _, err := connection.PrepareOpsManagerConnection(
		ctx, r.SecretClient, projectConfig, credentials,
		r.omConnectionFactory, opsManager.Namespace, log)
	if err != nil {
		return nil, construct.AgentConnectionConfig{}, workflow.Failed(xerrors.Errorf("failed to prepare external OM connection: %w", err))
	}

	opsManager.Status.AppDbStatus.ExternalGroupID = conn.GroupID()

	return conn, construct.AgentConnectionConfig{
		Enabled: true,
		Server:  projectConfig.BaseURL,
		GroupID: conn.GroupID(),
	}, workflow.OK()
}
```

Keep the old function body (including AC push, `stripUnsupportedACFields`, agent key logic) temporarily — it will be removed in PR 2 Task 8. For now, delete those steps from the old function only (lines that do the AC push) and ensure the new function compiles.

Actually: delete the old `reconcileManagedByMetaOM` function entirely and replace with `reconcileOMConnection` above. The AC push will be re-added in the right place in PR 2.

- [ ] **Step 4: Update the call site in `ReconcileAppDB`** (line ~664)

Replace:
```go
var metaOMEnvVars construct.MetaOMEnvVars
if opsManager.Spec.AppDB.ManagedByMetaOM != nil {
    var ws workflow.Status
    metaOMEnvVars, ws = r.reconcileManagedByMetaOM(ctx, opsManager, log)
    if !ws.IsOK() {
        return r.updateStatus(ctx, opsManager, ws, log, appDbStatusOption)
    }
}
appdbOpts.MetaOM = metaOMEnvVars
if appdbOpts.MetaOM.Enabled {
    podVars.ProjectID = appdbOpts.MetaOM.GroupID
    podVars.BaseURL = appdbOpts.MetaOM.Server
}
```

With:
```go
var agentConnConfig construct.AgentConnectionConfig
if opsManager.Spec.AppDB.Connection != nil {
    var ws workflow.Status
    _, agentConnConfig, ws = r.reconcileOMConnection(ctx, opsManager, log)
    if !ws.IsOK() {
        return r.updateStatus(ctx, opsManager, ws, log, appDbStatusOption)
    }
}
appdbOpts.Connection = agentConnConfig
if appdbOpts.Connection.Enabled {
    podVars.ProjectID = appdbOpts.Connection.GroupID
    podVars.BaseURL = appdbOpts.Connection.Server
}
```

- [ ] **Step 5: Fix all remaining `ManagedByMetaOM` references in `appdbreplicaset_controller.go`**

```bash
grep -n "ManagedByMetaOM\|MetaOMGroupID\|MetaOM\b\|reconcileManagedByMetaOM\|metaOMAgentKeySecretName" \
    controllers/operator/appdbreplicaset_controller.go
```

Replace each:
- `ManagedByMetaOM != nil` → `Connection != nil`
- `ManagedByMetaOM == nil` → `Connection == nil`
- `.MetaOMGroupID` → `.ExternalGroupID`
- `appdbOpts.MetaOM` → `appdbOpts.Connection`

Also delete the `metaOMAgentKeySecretName` helper function if it's no longer referenced.

- [ ] **Step 6: Build to check for remaining errors**

```bash
go build ./... 2>&1
```

Fix any remaining compile errors.

- [ ] **Step 7: Run the test**

```bash
go test ./controllers/operator/... -run TestReconcileOMConnection -v 2>&1
```

Expected: PASS.

- [ ] **Step 8: Run full unit test suite**

```bash
go test ./controllers/operator/... ./api/... 2>&1 | grep -E "FAIL|ok"
```

Expected: no FAILs.

- [ ] **Step 9: Regenerate CRDs**

```bash
make generate 2>&1 | tail -10
```

Expected: CRD YAML files updated in `config/crd/bases/`, `helm_chart/crds/`, `public/crds.yaml`.

- [ ] **Step 10: Commit**

```bash
git add controllers/operator/appdbreplicaset_controller.go \
        controllers/operator/appdbreplicaset_controller_test.go \
        config/crd/bases/ helm_chart/crds/ public/crds.yaml
git commit -m "feat: replace reconcileManagedByMetaOM with reconcileOMConnection using standard pipeline"
```

---

## PR 2 — Reconcile Order + AC Flow + Monitoring + Multi-cluster

---

### Task 5: Reorder reconcile loop — `reconcileOMConnection` before monitoring

**Files:**
- Modify: `controllers/operator/appdbreplicaset_controller.go`

Background: Currently `tryConfigureMonitoringInOpsManager` is called at line ~567, and the connection setup is at line ~664. They need to swap so the external OM connection is available before monitoring.

- [ ] **Step 1: Write a failing test verifying the order**

Add to `controllers/operator/appdbreplicaset_controller_test.go`:

```go
func TestReconcileAppDB_ConnectionSetupBeforeMonitoring(t *testing.T) {
	// When Connection is set, reconcileOMConnection must run before monitoring
	// is considered. Verify by checking that ExternalGroupID is populated on
	// the returned status (which would be empty if monitoring ran first and
	// short-circuited before connection setup).
	ctx := context.Background()
	om := omv1.NewOpsManagerBuilderDefault().Build()
	om.Spec.AppDB.Connection = &omv1.ConnectionSpec{
		SharedConnectionSpec: mdbv1.SharedConnectionSpec{
			OpsManagerConfig: mdbv1.OpsManagerConfig{
				ConfigMapRef: mdbv1.ConfigMapRef{Name: "appdb-project-config"},
			},
		},
		Credentials: "appdb-credentials",
	}
	// ... set up ConfigMap and credentials Secret as in Task 4 test ...
	// Run full ReconcileAppDB
	// Assert: ExternalGroupID is set in status
	// Assert: no monitoring sidecar (podVars.ProjectID should not drive sidecar creation)
	t.Skip("implement after reorder")
}
```

- [ ] **Step 2: Move the `reconcileOMConnection` call block to before the monitoring gate**

In `ReconcileAppDB`, move the block:
```go
var agentConnConfig construct.AgentConnectionConfig
if opsManager.Spec.AppDB.Connection != nil {
    var ws workflow.Status
    _, agentConnConfig, ws = r.reconcileOMConnection(ctx, opsManager, log)
    if !ws.IsOK() {
        return r.updateStatus(ctx, opsManager, ws, log, appDbStatusOption)
    }
}
appdbOpts.Connection = agentConnConfig
```

So that it appears **before** the monitoring gate block:
```go
if opsManager.Spec.AppDB.Connection == nil {
    podVars, err = r.tryConfigureMonitoringInOpsManager(...)
    ...
}
```

Also update the monitoring gate — change `ManagedByMetaOM == nil` to `Connection == nil` (already done in Task 4, but verify it's in the right position now).

- [ ] **Step 3: Build and run controller tests**

```bash
go build ./controllers/operator/... 2>&1
go test ./controllers/operator/... 2>&1 | grep -E "FAIL|ok"
```

Expected: no FAILs.

- [ ] **Step 4: Implement and unskip the test from Step 1, run it**

```bash
go test ./controllers/operator/... -run TestReconcileAppDB_ConnectionSetupBeforeMonitoring -v 2>&1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add controllers/operator/appdbreplicaset_controller.go \
        controllers/operator/appdbreplicaset_controller_test.go
git commit -m "refactor: run reconcileOMConnection before tryConfigureMonitoringInOpsManager"
```

---

### Task 6: Gate monitoring sidecar strictly to headless mode

**Files:**
- Modify: `controllers/operator/construct/appdb_construction.go:379-381`

Background: `ShouldEnableMonitoring` currently checks `podVars.ProjectID != ""`. In online mode `podVars.ProjectID` is set from `AgentConnectionConfig.GroupID` (line ~677), which would incorrectly enable the monitoring sidecar. The gate must be `Connection.Enabled == false`.

- [ ] **Step 1: Write failing tests**

Add to `controllers/operator/construct/appdb_construction_test.go`:

```go
func TestShouldEnableMonitoring_FalseInOnlineMode(t *testing.T) {
	// Even if podVars.ProjectID is set, monitoring sidecar is disabled in online mode.
	podVars := &env.PodEnvVars{ProjectID: "some-group-id"}
	opts := AppDBStatefulSetOptions{
		Connection: AgentConnectionConfig{Enabled: true, GroupID: "some-group-id"},
	}
	assert.False(t, ShouldEnableMonitoring(podVars, opts))
}

func TestShouldEnableMonitoring_TrueInHeadlessMode(t *testing.T) {
	podVars := &env.PodEnvVars{ProjectID: "some-group-id"}
	opts := AppDBStatefulSetOptions{
		Connection: AgentConnectionConfig{Enabled: false},
	}
	assert.True(t, ShouldEnableMonitoring(podVars, opts))
}

func TestShouldEnableMonitoring_FalseWhenNoPodVars(t *testing.T) {
	assert.False(t, ShouldEnableMonitoring(nil, AppDBStatefulSetOptions{}))
}
```

- [ ] **Step 2: Run to verify they fail**

```bash
go test ./controllers/operator/construct/... -run TestShouldEnableMonitoring -v 2>&1
```

Expected: compile error — `ShouldEnableMonitoring` signature mismatch.

- [ ] **Step 3: Update `ShouldEnableMonitoring`**

In `appdb_construction.go` at line ~379:

```go
// ShouldEnableMonitoring returns true if the monitoring sidecar container should be added.
// Monitoring sidecar is headless-mode only; online mode uses monitoringVersions in the AC.
func ShouldEnableMonitoring(podVars *env.PodEnvVars, opts AppDBStatefulSetOptions) bool {
	return GlobalMonitoringSettingEnabled() &&
		podVars != nil &&
		podVars.ProjectID != "" &&
		!opts.Connection.Enabled
}
```

Update `ShouldMountSSLMMSCAConfigMap` to pass `opts` if it calls `ShouldEnableMonitoring`:

```go
func ShouldMountSSLMMSCAConfigMap(podVars *env.PodEnvVars, opts AppDBStatefulSetOptions) bool {
	return ShouldEnableMonitoring(podVars, opts) && podVars.SSLMMSCAConfigMap != ""
}
```

- [ ] **Step 4: Fix all call sites for the updated signatures**

```bash
grep -rn "ShouldEnableMonitoring\|ShouldMountSSLMMSCAConfigMap" \
    controllers/ 2>/dev/null
```

Each call site needs `opts` passed as the second argument. The `opts` variable (`AppDBStatefulSetOptions`) is already in scope at every call site since it's threaded through `AppDbStatefulSet`.

- [ ] **Step 5: Run the tests**

```bash
go test ./controllers/operator/construct/... -run TestShouldEnableMonitoring -v 2>&1
```

Expected: PASS.

- [ ] **Step 6: Run full construction tests**

```bash
go test ./controllers/operator/construct/... -v 2>&1 | tail -20
```

Expected: all pass.

- [ ] **Step 7: Commit**

```bash
git add controllers/operator/construct/appdb_construction.go \
        controllers/operator/construct/appdb_construction_test.go
git commit -m "fix: gate monitoring sidecar to headless mode only via ShouldEnableMonitoring"
```

---

### Task 7: Move AC push into `deployAutomationConfig` — push to external OM when `om.Connection` is non-nil

**Files:**
- Modify: `controllers/operator/appdbreplicaset_controller.go`

Background: `deployAutomationConfig` (line 1903) currently always writes the AC to a Kubernetes Secret via `publishAutomationConfig`. For online mode, the AC must be pushed to the external OM API instead. The `om.Connection` returned by `reconcileOMConnection` is passed down to this function. No Secret write happens in online mode. The `deployAutomationConfigAndWaitForAgentsReachGoalState` signature gains one parameter.

- [ ] **Step 1: Write failing tests**

Add to `controllers/operator/appdbreplicaset_controller_test.go`:

```go
func TestDeployAutomationConfig_PushesToExternalOMWhenConnectionSet(t *testing.T) {
	// When externalConn is non-nil, UpdateAutomationConfig is called on it
	// and the Secret write is skipped.
	ctx := context.Background()
	om := omv1.NewOpsManagerBuilderDefault().Build()
	om.Spec.AppDB.Connection = &omv1.ConnectionSpec{
		Credentials: "creds",
	}

	mockConn := mocks.NewMockedOmConnection()
	reconciler, _, _ := defaultTestOmReconciler(ctx, t, nil, "", "", om, nil, nil)

	mc := reconciler.GetHealthyMemberClusters()[0]
	_, ws := reconciler.deployAutomationConfig(ctx, om, "", mc, mockConn, zaptest.NewLogger(t).Sugar())

	assert.True(t, ws.IsOK())
	assert.True(t, mockConn.UpdateAutomationConfigCalled,
		"UpdateAutomationConfig should be called on external connection")
}

func TestDeployAutomationConfig_WritesSecretWhenHeadless(t *testing.T) {
	ctx := context.Background()
	om := omv1.NewOpsManagerBuilderDefault().Build()
	// No Connection set = headless

	reconciler, _, _ := defaultTestOmReconciler(ctx, t, nil, "", "", om, nil, nil)
	mc := reconciler.GetHealthyMemberClusters()[0]
	_, ws := reconciler.deployAutomationConfig(ctx, om, "", mc, nil, zaptest.NewLogger(t).Sugar())

	assert.True(t, ws.IsOK())
	// Secret should exist in fake client
	s := &corev1.Secret{}
	err := reconciler.client.Get(ctx,
		kube.ObjectKey(om.Namespace, om.Spec.AppDB.AutomationConfigSecretName()), s)
	assert.NoError(t, err, "AC Secret should be written in headless mode")
}
```

- [ ] **Step 2: Run to verify they fail**

```bash
go test ./controllers/operator/... -run "TestDeployAutomationConfig" -v 2>&1 | head -20
```

Expected: compile error — `deployAutomationConfig` signature mismatch.

- [ ] **Step 3: Update `deployAutomationConfig` signature and body**

In `appdbreplicaset_controller.go` at line 1903, update the function:

```go
func (r *ReconcileAppDbReplicaSet) deployAutomationConfig(
	ctx context.Context,
	opsManager *omv1.MongoDBOpsManager,
	prometheusCertHash string,
	memberCluster multicluster.MemberCluster,
	externalConn om.Connection, // non-nil → push to external OM, skip Secret write
	log *zap.SugaredLogger,
) (int, workflow.Status) {
	rs := opsManager.Spec.AppDB

	config, err := r.buildAppDbAutomationConfig(ctx, opsManager, automation, prometheusCertHash, memberCluster.Name, log)
	if err != nil {
		return 0, workflow.Failed(err)
	}

	if externalConn != nil {
		// TODO: verify with e2e and remove stripUnsupportedACFields if fresh-built AC needs no sanitisation
		if err := externalConn.UpdateAutomationConfig(config, log); err != nil {
			return 0, workflow.Failed(xerrors.Errorf("failed to push AC to external OM: %w", err))
		}
		return config.Version, workflow.OK()
	}

	var configVersion int
	if configVersion, err = r.publishAutomationConfig(ctx, opsManager, config, rs.AutomationConfigSecretName(), memberCluster.SecretClient); err != nil {
		return 0, workflow.Failed(err)
	}
	if workflowStatus := r.publishACVersionAsConfigMap(ctx, opsManager.Spec.AppDB.AutomationConfigConfigMapName(), opsManager, configVersion, memberCluster); !workflowStatus.IsOK() {
		return 0, workflowStatus
	}

	if err := r.deployMonitoringAgentAutomationConfig(ctx, opsManager, memberCluster, log); err != nil {
		return 0, workflow.Failed(err)
	}
	monitoringAc, err := r.buildAppDbAutomationConfig(ctx, opsManager, monitoring, UnusedPrometheusConfiguration, memberCluster.Name, log)
	if err != nil {
		return 0, workflow.Failed(err)
	}
	if workflowStatus := r.publishACVersionAsConfigMap(ctx, opsManager.Spec.AppDB.MonitoringAutomationConfigConfigMapName(), opsManager, monitoringAc.Version, memberCluster); !workflowStatus.IsOK() {
		return 0, workflowStatus
	}

	return configVersion, workflow.OK()
}
```

- [ ] **Step 4: Update `deployAutomationConfigAndWaitForAgentsReachGoalState` to accept and thread `om.Connection`**

```go
func (r *ReconcileAppDbReplicaSet) deployAutomationConfigAndWaitForAgentsReachGoalState(
	ctx context.Context,
	log *zap.SugaredLogger,
	opsManager *omv1.MongoDBOpsManager,
	externalConn om.Connection,
	allStatefulSetsExist bool,
	appdbOpts construct.AppDBStatefulSetOptions,
) workflow.Status
```

Inside, pass `externalConn` to `deployAutomationConfig` calls.

- [ ] **Step 5: Update the call site of `deployAutomationConfigAndWaitForAgentsReachGoalState` in `ReconcileAppDB`**

The `externalConn` variable is the one returned by `reconcileOMConnection` in the block added in Task 4 (it was discarded with `_` — update to capture it):

```go
var externalConn om.Connection
var agentConnConfig construct.AgentConnectionConfig
if opsManager.Spec.AppDB.Connection != nil {
    var ws workflow.Status
    externalConn, agentConnConfig, ws = r.reconcileOMConnection(ctx, opsManager, log)
    if !ws.IsOK() {
        return r.updateStatus(ctx, opsManager, ws, log, appDbStatusOption)
    }
}
```

Pass `externalConn` to `deployAutomationConfigAndWaitForAgentsReachGoalState`.

- [ ] **Step 6: Run the tests**

```bash
go test ./controllers/operator/... -run "TestDeployAutomationConfig" -v 2>&1
```

Expected: PASS.

- [ ] **Step 7: Run full suite**

```bash
go test ./controllers/operator/... 2>&1 | grep -E "FAIL|ok"
```

Expected: no FAILs.

- [ ] **Step 8: Commit**

```bash
git add controllers/operator/appdbreplicaset_controller.go \
        controllers/operator/appdbreplicaset_controller_test.go
git commit -m "feat: push AC to external OM in deployAutomationConfig when Connection is set"
```

---

### Task 8: Multi-cluster — replicate agent key Secret to all member clusters

**Files:**
- Modify: `controllers/operator/appdbreplicaset_controller.go`

Background: `PrepareOpsManagerConnection` creates `{groupId}-group-secret` in the operator namespace (central cluster only). AppDB pods in other member clusters need to mount this Secret. Follow the same pattern as `replicateTLSCAConfigMap` (line 1028).

- [ ] **Step 1: Write failing test**

Add to `controllers/operator/appdbreplicaset_controller_test.go`:

```go
func TestReplicateAgentKeySecret_CopiesSecretToAllMemberClusters(t *testing.T) {
	ctx := context.Background()
	om := omv1.NewOpsManagerBuilderDefault().Build()
	// set up a multi-cluster topology
	groupID := "aabbcc112233"
	secretName := agents.ApiKeySecretName(groupID) // "{groupID}-group-secret"

	// Pre-create the secret in the central cluster (as PrepareOpsManagerConnection would)
	keySecret := secret.Builder().
		SetName(secretName).
		SetNamespace(om.Namespace).
		SetField("agentApiKey", "secret-key-value").
		Build()

	reconciler, _, _ := defaultTestOmReconciler(ctx, t, nil, "", "", om, nil, nil)
	require.NoError(t, reconciler.SecretClient.CreateSecret(ctx, keySecret))

	err := reconciler.replicateAgentKeySecret(ctx, om, groupID, zaptest.NewLogger(t).Sugar())
	require.NoError(t, err)

	// Assert the secret exists in each member cluster
	for _, mc := range reconciler.GetHealthyMemberClusters() {
		s, err := mc.SecretClient.ReadSecret(ctx, kube.ObjectKey(om.Namespace, secretName), "")
		assert.NoError(t, err, "agent key secret should exist in member cluster %s", mc.Name)
		assert.Equal(t, "secret-key-value", s["agentApiKey"])
	}
}
```

- [ ] **Step 2: Run to verify it fails**

```bash
go test ./controllers/operator/... -run TestReplicateAgentKeySecret -v 2>&1 | head -10
```

Expected: compile error — `replicateAgentKeySecret` not defined.

- [ ] **Step 3: Implement `replicateAgentKeySecret`**

Add to `appdbreplicaset_controller.go` after `replicateTLSCAConfigMap`:

```go
// replicateAgentKeySecret copies the agent API key Secret (created by PrepareOpsManagerConnection
// in the central cluster) to all healthy member clusters so each AppDB pod can mount it.
func (r *ReconcileAppDbReplicaSet) replicateAgentKeySecret(
	ctx context.Context,
	opsManager *omv1.MongoDBOpsManager,
	groupID string,
	log *zap.SugaredLogger,
) error {
	if !opsManager.Spec.AppDB.IsMultiCluster() {
		return nil
	}
	secretName := agents.ApiKeySecretName(groupID)
	keyData, err := r.SecretClient.ReadSecret(ctx, kube.ObjectKey(opsManager.Namespace, secretName), "")
	if err != nil {
		return xerrors.Errorf("failed to read agent key secret %s: %w", secretName, err)
	}
	for _, mc := range r.GetHealthyMemberClusters() {
		if err := mc.SecretClient.CreateOrUpdateSecret(ctx, opsManager.Namespace, secretName, keyData); err != nil {
			return xerrors.Errorf("failed to replicate agent key secret to cluster %s: %w", mc.Name, err)
		}
	}
	return nil
}
```

- [ ] **Step 4: Call `replicateAgentKeySecret` in `ReconcileAppDB` after `reconcileOMConnection`**

```go
if opsManager.Spec.AppDB.Connection != nil {
    var ws workflow.Status
    externalConn, agentConnConfig, ws = r.reconcileOMConnection(ctx, opsManager, log)
    if !ws.IsOK() {
        return r.updateStatus(ctx, opsManager, ws, log, appDbStatusOption)
    }
    if err := r.replicateAgentKeySecret(ctx, opsManager, agentConnConfig.GroupID, log); err != nil {
        return r.updateStatus(ctx, opsManager,
            workflow.Failed(xerrors.Errorf("failed to replicate agent key: %w", err)),
            log, appDbStatusOption)
    }
}
```

- [ ] **Step 5: Run the test**

```bash
go test ./controllers/operator/... -run TestReplicateAgentKeySecret -v 2>&1
```

Expected: PASS.

- [ ] **Step 6: Run full suite**

```bash
go test ./controllers/operator/... 2>&1 | grep -E "FAIL|ok"
```

Expected: no FAILs.

- [ ] **Step 7: Commit**

```bash
git add controllers/operator/appdbreplicaset_controller.go \
        controllers/operator/appdbreplicaset_controller_test.go
git commit -m "feat: replicate agent key Secret to all member clusters in online mode"
```

---

### Task 9: Update e2e test fixtures and Python test references

**Files:**
- Modify: `docker/mongodb-kubernetes-tests/tests/opsmanager/fixtures/om_appdb_switch_primary_om.yaml`
- Modify: `docker/mongodb-kubernetes-tests/tests/opsmanager/om_appdb_meta_om_mode_switch.py`

Background: The YAML fixture uses `managedByMetaOM` (old field). The Python test references the agent key Secret by old name and may reference `MetaOMGroupID` status field.

- [ ] **Step 1: Find all references to rename**

```bash
grep -rn "managedByMetaOM\|metaOMGroupId\|meta-om-agent-key\|ManagedByMetaOM\|MetaOMGroupID" \
    docker/mongodb-kubernetes-tests/tests/opsmanager/ 2>/dev/null
```

- [ ] **Step 2: Update the YAML fixture**

In `docker/mongodb-kubernetes-tests/tests/opsmanager/fixtures/om_appdb_switch_primary_om.yaml`, rename the field:

```yaml
# Before:
spec:
  applicationDatabase:
    managedByMetaOM:
      name: om-meta
      projectName: primary-appdb
      credentialsSecretRef:
        name: meta-om-creds

# After:
spec:
  applicationDatabase:
    connection:
      opsManager:
        configMapRef:
          name: primary-appdb-project-config
      credentials: meta-om-creds
```

Note: the new format uses a standard project ConfigMap instead of CR name reference. Ensure the test creates the `primary-appdb-project-config` ConfigMap with the external OM URL and project name before patching the CR.

- [ ] **Step 3: Update the Python test**

In `om_appdb_meta_om_mode_switch.py`, update:
- Any reference to agent key secret `*-meta-om-agent-key` → `{groupId}-group-secret` (use `agents.ApiKeySecretName` pattern: `f"{group_id}-group-secret"`)
- Any assertion on `status.applicationDatabase.metaOMGroupId` → `status.applicationDatabase.externalGroupID`
- Fixture patching code: update the `managedByMetaOM` dict key → use the new `connection` structure

- [ ] **Step 4: Verify ty check passes**

```bash
cd docker/mongodb-kubernetes-tests && uvx ty check tests/ 2>&1
```

Expected: `All checks passed!`

- [ ] **Step 5: Verify pre-commit passes**

```bash
cd /path/to/repo && pre-commit run --all-files 2>&1 | grep -E "Failed|Passed"
```

Expected: all Passed.

- [ ] **Step 6: Commit**

```bash
git add docker/mongodb-kubernetes-tests/tests/opsmanager/
git commit -m "test: update e2e fixtures and test references for connection field rename"
```

---

### Task 10: Final verification

- [ ] **Step 1: Full build**

```bash
go build ./... 2>&1
```

Expected: no errors.

- [ ] **Step 2: Full unit test suite**

```bash
go test ./... 2>&1 | grep -E "FAIL|ok"
```

Expected: no FAILs.

- [ ] **Step 3: Verify CRDs are up to date**

```bash
make generate 2>&1 | tail -5
git diff --name-only config/crd/bases/ helm_chart/crds/ public/crds.yaml
```

Expected: no uncommitted CRD changes (or commit them if there are any).

- [ ] **Step 4: Run pre-commit**

```bash
pre-commit run --all-files 2>&1
```

Expected: all hooks pass.
