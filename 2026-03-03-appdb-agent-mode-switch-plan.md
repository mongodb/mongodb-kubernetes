# AppDB Agent Mode Switch Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Allow an existing Ops Manager AppDB running in headless mode to be transitioned in-place to online mode managed by a secondary (Meta) Ops Manager, enabling backup via Meta OM without data migration.

**Architecture:** A new optional field `spec.applicationDatabase.managedByMetaOM` on the `MongoDBOpsManager` CR triggers a new reconciliation branch in `ReconcileAppDbReplicaSet`. The reconciler idempotently creates a Meta OM project, optionally migrates the initial Automation Config, provisions an agent API key Secret, and updates the AppDB StatefulSet env vars to remove headless mode and point agents at Meta OM. The existing readiness probe handles pod-ready signalling — no polling needed.

**Tech Stack:** Go, controller-runtime, kubebuilder markers, testify, existing `om.ConnectionFactory` / `om.Connection` interfaces.

**Spike doc:** `2026-03-03-appdb-agent-mode-switch-spike.md` at repo root.

---

## Background: Key Files

| File | Purpose |
|---|---|
| `api/v1/om/appdb_types.go` | `AppDBSpec` struct — add `ManagedByMetaOM` field here |
| `api/v1/om/opsmanager_types.go` | `MongoDBOpsManagerSpec`, `AppDbStatus` — add `MetaOMGroupId` to status |
| `controllers/operator/construct/appdb_construction.go` | `appdbContainerEnv()` (line ~635), `AppDBStatefulSetOptions` (line 51), `AppDbStatefulSet()` (line 375) |
| `controllers/operator/construct/appdb_construction_test.go` | Unit tests for StatefulSet construction |
| `controllers/operator/appdbreplicaset_controller.go` | `ReconcileAppDbReplicaSet` struct (line 114), `ReconcileAppDB()` (line 521) |
| `controllers/operator/appdbreplicaset_controller_test.go` | Unit tests for AppDB reconciler |
| `controllers/om/omclient.go` | `ConnectionFactory`, `OMContext`, `Connection` interface |
| `controllers/om/group.go` | `Project` struct with `AgentAPIKey` field |

## Background: Existing OM Connection Pattern

To call Meta OM's API, reuse the existing factory:
```go
// r.omConnectionFactory is func(*OMContext) Connection
ctx := &om.OMContext{
    BaseURL:    metaOM.CentralURL(),   // from the Meta OM CR
    PublicKey:  creds.PublicKey,       // from credentialsSecretRef Secret
    PrivateKey: creds.PrivateKey,
}
conn := r.omConnectionFactory(ctx)
// conn.CreateProject(...), conn.ReadAutomationConfig(), etc.
```

## Background: How Headless Env Vars Are Set Today

`appdbContainerEnv()` in `appdb_construction.go` (line 635) always returns:
- `AUTOMATION_CONFIG_MAP = {appdb-name}-config`
- `HEADLESS_AGENT = true`

These must be replaced with `MMS_SERVER`, `MMS_GROUP_ID`, `MMS_API_KEY` when `ManagedByMetaOM` is set.

---

## Task 1: Add API Types

**Files:**
- Modify: `api/v1/om/appdb_types.go`
- Modify: `api/v1/om/opsmanager_types.go`

### Step 1: Add `MetaOMRef` struct and `ManagedByMetaOM` field to `AppDBSpec`

Open `api/v1/om/appdb_types.go`. After the existing `AppDBSpec` struct definition, add the new struct and field.

Add `MetaOMRef` struct (can be placed near the bottom of the file before the closing of the types section):

```go
// MetaOMRef references a secondary (Meta) Ops Manager instance that will
// take over management of the AppDB agents, enabling backup via Meta OM.
type MetaOMRef struct {
    // Name of the MongoDBOpsManager CR acting as Meta OM.
    // +kubebuilder:validation:Required
    Name string `json:"name"`

    // Namespace of the Meta OM CR. Defaults to the same namespace as Primary OM.
    // +optional
    Namespace string `json:"namespace,omitempty"`

    // ProjectName is the name of the project to create or use in Meta OM
    // for this AppDB deployment.
    // +kubebuilder:validation:Required
    ProjectName string `json:"projectName"`

    // CredentialsSecretRef references a Secret containing Meta OM admin
    // API credentials. The Secret must have keys "publicKey" and "privateKey".
    // +kubebuilder:validation:Required
    CredentialsSecretRef corev1.LocalObjectReference `json:"credentialsSecretRef"`
}
```

Inside `AppDBSpec`, add the new field at the end (before the closing brace):

```go
// ManagedByMetaOM, when set, transitions AppDB agents from headless mode
// to online mode managed by the referenced secondary (Meta) Ops Manager.
// +optional
ManagedByMetaOM *MetaOMRef `json:"managedByMetaOM,omitempty"`
```

### Step 2: Add `MetaOMGroupId` to `AppDbStatus`

Open `api/v1/om/opsmanager_types.go`. Find `AppDbStatus` (currently around line 474):

```go
type AppDbStatus struct {
    mdbv1.MongoDbStatus `json:",inline"`
    ClusterStatusList   []status.ClusterStatusItem `json:"clusterStatusList,omitempty"`
}
```

Add the new field:

```go
type AppDbStatus struct {
    mdbv1.MongoDbStatus `json:",inline"`
    ClusterStatusList   []status.ClusterStatusItem `json:"clusterStatusList,omitempty"`
    // MetaOMGroupId stores the Meta OM project (group) ID once the AppDB
    // has been registered with Meta OM. Used by subsequent reconciliation steps.
    // +optional
    MetaOMGroupId string `json:"metaOMGroupId,omitempty"`
}
```

### Step 3: Regenerate CRD manifests

```bash
make generate
```

Expected: no errors; updated CRD YAML in `config/crd/bases/`.

### Step 4: Verify the build compiles

```bash
go build ./...
```

Expected: no compilation errors.

### Step 5: Commit

```bash
git add api/v1/om/appdb_types.go api/v1/om/opsmanager_types.go config/crd/bases/
git commit -m "feat: add MetaOMRef type and ManagedByMetaOM field to AppDBSpec

Co-authored-by: Copilot <223556219+Copilot@users.noreply.github.com>"
```

---

## Task 2: Update StatefulSet Construction

**Files:**
- Modify: `controllers/operator/construct/appdb_construction.go`
- Modify: `controllers/operator/construct/appdb_construction_test.go`

### Step 1: Write failing unit tests

Open `controllers/operator/construct/appdb_construction_test.go`.

Add the following tests (follow the existing `TestResourceRequirements` pattern — use `omv1.NewOpsManagerBuilderDefault().Build()` and call `AppDbStatefulSet(...)`):

```go
func TestAppdbContainerEnv_HeadlessMode(t *testing.T) {
    // When ManagedByMetaOM is NOT set, headless env vars must be present
    om := omv1.NewOpsManagerBuilderDefault().Build()

    sts, err := AppDbStatefulSet(*om, &env.PodEnvVars{ProjectID: "abcd"},
        AppDBStatefulSetOptions{}, scalers.GetAppDBScaler(om, "central", 0, nil),
        v1.OnDeleteStatefulSetStrategyType, nil)
    require.NoError(t, err)

    agentContainer := findContainer(t, sts, construct.AgentName)
    assertEnvVarPresent(t, agentContainer.Env, "HEADLESS_AGENT", "true")
    assertEnvVarPresent(t, agentContainer.Env, "AUTOMATION_CONFIG_MAP", om.Spec.AppDB.Name()+"-config")
    assertEnvVarAbsent(t, agentContainer.Env, "MMS_SERVER")
    assertEnvVarAbsent(t, agentContainer.Env, "MMS_GROUP_ID")
    assertEnvVarAbsent(t, agentContainer.Env, "MMS_API_KEY")
}

func TestAppdbContainerEnv_MetaOMMode(t *testing.T) {
    // When ManagedByMetaOM IS set and MetaOM vars provided, online env vars must be present
    om := omv1.NewOpsManagerBuilderDefault().Build()
    om.Spec.AppDB.ManagedByMetaOM = &omv1.MetaOMRef{
        Name:        "om-meta",
        ProjectName: "primary-appdb",
        CredentialsSecretRef: corev1.LocalObjectReference{Name: "meta-om-creds"},
    }

    opts := AppDBStatefulSetOptions{
        MetaOMServer:  "http://om-meta-svc.meta-ns.svc.cluster.local:8080",
        MetaOMGroupID: "aabbccdd112233445566",
        MetaOMAPIKey:  "secret-agent-key",
    }

    sts, err := AppDbStatefulSet(*om, &env.PodEnvVars{ProjectID: "abcd"},
        opts, scalers.GetAppDBScaler(om, "central", 0, nil),
        v1.OnDeleteStatefulSetStrategyType, nil)
    require.NoError(t, err)

    agentContainer := findContainer(t, sts, construct.AgentName)
    assertEnvVarAbsent(t, agentContainer.Env, "HEADLESS_AGENT")
    assertEnvVarAbsent(t, agentContainer.Env, "AUTOMATION_CONFIG_MAP")
    assertEnvVarPresent(t, agentContainer.Env, "MMS_SERVER", opts.MetaOMServer)
    assertEnvVarPresent(t, agentContainer.Env, "MMS_GROUP_ID", opts.MetaOMGroupID)
    assertEnvVarPresent(t, agentContainer.Env, "MMS_API_KEY", opts.MetaOMAPIKey)
}

// helpers — add these at the bottom of the test file
func findContainer(t *testing.T, sts appsv1.StatefulSet, name string) corev1.Container {
    t.Helper()
    for _, c := range sts.Spec.Template.Spec.Containers {
        if c.Name == name {
            return c
        }
    }
    t.Fatalf("container %q not found in StatefulSet", name)
    return corev1.Container{}
}

func assertEnvVarPresent(t *testing.T, envVars []corev1.EnvVar, name, value string) {
    t.Helper()
    for _, e := range envVars {
        if e.Name == name {
            assert.Equal(t, value, e.Value, "env var %q has unexpected value", name)
            return
        }
    }
    t.Errorf("env var %q not found", name)
}

func assertEnvVarAbsent(t *testing.T, envVars []corev1.EnvVar, name string) {
    t.Helper()
    for _, e := range envVars {
        if e.Name == name {
            t.Errorf("env var %q should not be present but was found with value %q", name, e.Value)
        }
    }
}
```

### Step 2: Run tests to verify they fail

```bash
go test ./controllers/operator/construct/... -run "TestAppdbContainerEnv" -v
```

Expected: `FAIL` — `AppDBStatefulSetOptions` has no `MetaOMServer`/`MetaOMGroupID`/`MetaOMAPIKey` fields yet.

### Step 3: Add Meta OM fields to `AppDBStatefulSetOptions`

Open `controllers/operator/construct/appdb_construction.go`. Find `AppDBStatefulSetOptions` (line 51) and add three new fields:

```go
type AppDBStatefulSetOptions struct {
    VaultConfig vault.VaultConfiguration
    CertHash    string

    InitAppDBImage             string
    MongodbImage               string
    AgentImage                 string
    LegacyMonitoringAgentImage string

    PrometheusTLSCertHash string

    // MetaOM connection env vars. When all three are non-empty, the StatefulSet
    // is built in online mode (no HEADLESS_AGENT / AUTOMATION_CONFIG_MAP).
    MetaOMServer  string
    MetaOMGroupID string
    MetaOMAPIKey  string
}
```

### Step 4: Update `appdbContainerEnv` to be conditional

Find `appdbContainerEnv` (line ~635). Replace the current implementation with:

```go
// appdbContainerEnv returns env vars for the AppDB agent container.
// When metaOM vars are provided (all non-empty), online mode env vars are returned.
// Otherwise headless mode env vars are returned.
func appdbContainerEnv(appDbSpec om.AppDBSpec, opts AppDBStatefulSetOptions) []corev1.EnvVar {
    envVars := []corev1.EnvVar{
        {
            Name:      podNamespaceEnv,
            ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.namespace"}},
        },
        {
            Name:  clusterDomainEnv,
            Value: appDbSpec.ClusterDomain,
        },
    }

    if opts.MetaOMServer != "" && opts.MetaOMGroupID != "" && opts.MetaOMAPIKey != "" {
        envVars = append(envVars,
            corev1.EnvVar{Name: "MMS_SERVER", Value: opts.MetaOMServer},
            corev1.EnvVar{Name: "MMS_GROUP_ID", Value: opts.MetaOMGroupID},
            corev1.EnvVar{Name: "MMS_API_KEY", Value: opts.MetaOMAPIKey},
        )
    } else {
        envVars = append(envVars,
            corev1.EnvVar{Name: automationConfigMapEnv, Value: appDbSpec.Name() + "-config"},
            corev1.EnvVar{Name: headlessAgentEnv, Value: "true"},
        )
    }
    return envVars
}
```

### Step 5: Update the call site to pass `opts`

Find line ~441 where `appdbContainerEnv` is called:

```go
container.WithEnvs(appdbContainerEnv(*appDb)...),
```

Change it to pass `opts`:

```go
container.WithEnvs(appdbContainerEnv(*appDb, opts)...),
```

> Note: `opts` is already in scope as a parameter of `AppDbStatefulSet`. If there is a second call site (line ~627 in `addMonitoringContainer`), check whether it also needs updating. The monitoring container typically uses different env vars — verify and update only if `appdbContainerEnv` is called there for the agent container.

### Step 6: Run tests to verify they pass

```bash
go test ./controllers/operator/construct/... -run "TestAppdbContainerEnv" -v
```

Expected: `PASS`

### Step 7: Run full construction tests to catch regressions

```bash
go test ./controllers/operator/construct/... -v
```

Expected: all existing tests still pass.

### Step 8: Commit

```bash
git add controllers/operator/construct/appdb_construction.go \
        controllers/operator/construct/appdb_construction_test.go
git commit -m "feat: conditional env vars in AppDB StatefulSet for Meta OM online mode

Co-authored-by: Copilot <223556219+Copilot@users.noreply.github.com>"
```

---

## Task 3: Implement `reconcileManagedByMetaOM()`

**Files:**
- Modify: `controllers/operator/appdbreplicaset_controller.go`
- Modify: `controllers/operator/appdbreplicaset_controller_test.go`

This is the core reconciliation logic. The function must be **fully idempotent** — each step checks actual resource state.

### Step 1: Write failing unit tests

Open `controllers/operator/appdbreplicaset_controller_test.go`. Add the following tests.

> Setup hint: Tests in this file use `DefaultOpsManagerBuilder()` / `NewOpsManagerBuilderDefault()`. Look at existing tests like `TestMongoDB_ConnectionURL_DefaultCluster_AppDB` to understand how to construct an `opsManager` and a test reconciler via `defaultTestOmReconciler(...)`.

```go
func TestReconcileManagedByMetaOM_CreatesProjectAndStoresGroupId(t *testing.T) {
    ctx := context.Background()
    // Build Primary OM with managedByMetaOM set
    primaryOM := omv1.NewOpsManagerBuilderDefault().Build()
    primaryOM.Spec.AppDB.ManagedByMetaOM = &omv1.MetaOMRef{
        Name:        "om-meta",
        ProjectName: "primary-appdb",
        CredentialsSecretRef: corev1.LocalObjectReference{Name: "meta-om-creds"},
    }

    // Build Meta OM CR (Running phase)
    metaOM := omv1.NewOpsManagerBuilderDefault().Build()
    metaOM.Name = "om-meta"
    metaOM.Namespace = primaryOM.Namespace
    metaOM.Status.OpsManagerStatus.Phase = status.PhaseRunning

    // Credentials secret
    credSecret := secret.Builder().
        SetName("meta-om-creds").
        SetNamespace(primaryOM.Namespace).
        SetField("publicKey", "pub").
        SetField("privateKey", "priv").
        Build()

    // Mock OM connection factory — returns a mock connection that returns a project
    // with a known groupId when CreateProject is called
    // (adapt to the mock pattern used in this test file)
    mockConn := om.NewMockedOmConnection()
    mockConn.GroupId = "aabbcc112233"

    reconciler, _, _ := defaultTestOmReconciler(ctx, t, nil, "", "", primaryOM,
        []*omv1.MongoDBOpsManager{metaOM}, om.NewMockedConnectionFactory(mockConn))

    // Pre-create the credentials secret in the fake client
    require.NoError(t, reconciler.client.Create(ctx, credSecret))

    result, err := reconciler.ReconcileAppDB(ctx, primaryOM)
    require.NoError(t, err)
    assert.False(t, result.Requeue)

    // Assert groupId stored in status
    updatedOM := &omv1.MongoDBOpsManager{}
    require.NoError(t, reconciler.client.Get(ctx, kube.ObjectKey(primaryOM.Namespace, primaryOM.Name), updatedOM))
    assert.Equal(t, "aabbcc112233", updatedOM.Status.AppDbStatus.MetaOMGroupId)
}

func TestReconcileManagedByMetaOM_PushesConfigOnlyWhenMetaOMConfigEmpty(t *testing.T) {
    // When Meta OM project has empty automation config AND headless secret exists,
    // the config should be POSTed to Meta OM exactly once.
    // When Meta OM already has a non-empty config, the POST must NOT happen.
    // (Use mock connection that tracks whether UpdateAutomationConfig was called)
    ctx := context.Background()
    // ... setup similar to above; assert mock.UpdateAutomationConfigCalled == true
    // then repeat with non-empty config in mock and assert == false
    t.Skip("implement with mock tracking")
}

func TestReconcileManagedByMetaOM_CreatesAgentKeySecret(t *testing.T) {
    // When agent key secret does not exist, it must be created with the key from Meta OM
    ctx := context.Background()
    // ... setup; assert secret {appdb-name}-meta-om-agent-key exists after reconcile
    t.Skip("implement with mock tracking")
}

func TestReconcileManagedByMetaOM_Idempotent(t *testing.T) {
    // Running reconcile twice must not call CreateProject or UpdateAutomationConfig
    // a second time (project already exists, config already non-empty, secret exists)
    ctx := context.Background()
    // ... setup; run ReconcileAppDB twice; assert API calls == 1 each
    t.Skip("implement with call-count assertions")
}

func TestReconcileManagedByMetaOM_RequeuesWhenMetaOMNotRunning(t *testing.T) {
    ctx := context.Background()
    primaryOM := omv1.NewOpsManagerBuilderDefault().Build()
    primaryOM.Spec.AppDB.ManagedByMetaOM = &omv1.MetaOMRef{
        Name:        "om-meta",
        ProjectName: "primary-appdb",
        CredentialsSecretRef: corev1.LocalObjectReference{Name: "meta-om-creds"},
    }
    metaOM := omv1.NewOpsManagerBuilderDefault().Build()
    metaOM.Name = "om-meta"
    metaOM.Namespace = primaryOM.Namespace
    metaOM.Status.OpsManagerStatus.Phase = status.PhasePending // not Running

    // ... setup reconciler with metaOM in Pending phase
    // result should requeue
    t.Skip("implement")
}
```

### Step 2: Run tests to verify they fail (or skip is hit)

```bash
go test ./controllers/operator/... -run "TestReconcileManagedByMetaOM" -v
```

Expected: tests are skipped or fail with "function not defined".

### Step 3: Implement `reconcileManagedByMetaOM()`

Add the following function to `controllers/operator/appdbreplicaset_controller.go`.

Place it after `ReconcileAppDB()` and before any helper functions at the bottom of the file.

```go
// metaOMAgentKeySecretName returns the name of the Secret that stores the Meta OM agent API key.
func metaOMAgentKeySecretName(appDBName string) string {
    return appDBName + "-meta-om-agent-key"
}

// reconcileManagedByMetaOM performs the idempotent steps to transition the AppDB
// from headless mode to online mode managed by the referenced Meta OM instance.
// It returns the Meta OM connection env vars to be injected into the AppDB StatefulSet,
// or an error/workflow.Status if the reconciliation should be retried.
func (r *ReconcileAppDbReplicaSet) reconcileManagedByMetaOM(
    ctx context.Context,
    opsManager *omv1.MongoDBOpsManager,
    log *zap.SugaredLogger,
) (construct.MetaOMEnvVars, workflow.Status) {
    ref := opsManager.Spec.AppDB.ManagedByMetaOM
    appDBName := opsManager.Spec.AppDB.Name()
    namespace := ref.Namespace
    if namespace == "" {
        namespace = opsManager.Namespace
    }

    // Step 1: Look up Meta OM CR and verify it is Running.
    metaOM := &omv1.MongoDBOpsManager{}
    if err := r.client.Get(ctx, kube.ObjectKey(namespace, ref.Name), metaOM); err != nil {
        return construct.MetaOMEnvVars{}, workflow.Failed(fmt.Errorf("failed to get Meta OM CR %s/%s: %w", namespace, ref.Name, err))
    }
    if metaOM.Status.OpsManagerStatus.Phase != status.PhaseRunning {
        return construct.MetaOMEnvVars{}, workflow.Pending(fmt.Sprintf("Meta OM %s/%s is not yet Running (phase: %s)", namespace, ref.Name, metaOM.Status.OpsManagerStatus.Phase))
    }

    // Step 2: Read credentials secret.
    credSecret, err := secret.ReadSecret(ctx, r.client, kube.ObjectKey(opsManager.Namespace, ref.CredentialsSecretRef.Name))
    if err != nil {
        return construct.MetaOMEnvVars{}, workflow.Failed(fmt.Errorf("failed to read Meta OM credentials secret %q: %w", ref.CredentialsSecretRef.Name, err))
    }
    publicKey := credSecret["publicKey"]
    privateKey := credSecret["privateKey"]

    // Step 3: Create or retrieve the Meta OM project.
    omCtx := &om.OMContext{
        BaseURL:    metaOM.CentralURL(),
        GroupName:  ref.ProjectName,
        PublicKey:  publicKey,
        PrivateKey: privateKey,
    }
    conn := r.omConnectionFactory(omCtx)

    project, err := conn.GetOrCreateProject(ref.ProjectName, log)
    if err != nil {
        return construct.MetaOMEnvVars{}, workflow.Failed(fmt.Errorf("failed to get/create Meta OM project %q: %w", ref.ProjectName, err))
    }
    groupId := project.ID

    // Persist groupId in status for observability.
    opsManager.Status.AppDbStatus.MetaOMGroupId = groupId
    if err := r.updateStatusOnly(ctx, opsManager); err != nil {
        log.Warnw("Failed to persist MetaOMGroupId in status", "err", err)
    }

    // Step 4: Optionally push Automation Config to Meta OM (only if Meta OM config is empty).
    headlessConfigSecret, err := secret.ReadSecret(ctx, r.client, kube.ObjectKey(opsManager.Namespace, appDBName+"-config"))
    if err == nil && len(headlessConfigSecret) > 0 {
        // Secret exists — check if Meta OM already has a config.
        // Use a connection scoped to the new project groupId.
        projectConn := r.omConnectionFactory(&om.OMContext{
            BaseURL:    metaOM.CentralURL(),
            GroupID:    groupId,
            PublicKey:  publicKey,
            PrivateKey: privateKey,
        })
        existingAC, err := projectConn.ReadAutomationConfig()
        if err != nil {
            return construct.MetaOMEnvVars{}, workflow.Failed(fmt.Errorf("failed to read automation config from Meta OM: %w", err))
        }
        if existingAC.IsEmpty() {
            // Parse and push the headless config.
            var ac automationconfig.AutomationConfig
            if err := json.Unmarshal([]byte(headlessConfigSecret["automation-config"]), &ac); err != nil {
                return construct.MetaOMEnvVars{}, workflow.Failed(fmt.Errorf("failed to parse headless Automation Config: %w", err))
            }
            if err := projectConn.UpdateAutomationConfig(&ac, log); err != nil {
                return construct.MetaOMEnvVars{}, workflow.Failed(fmt.Errorf("failed to push Automation Config to Meta OM: %w", err))
            }
            log.Infow("Pushed initial Automation Config to Meta OM", "project", ref.ProjectName)
        }
    }

    // Step 5: Ensure agent API key Secret exists.
    agentKeySecretName := metaOMAgentKeySecretName(appDBName)
    agentKey, err := secret.ReadStringKey(ctx, r.client, kube.ObjectKey(opsManager.Namespace, agentKeySecretName), "agentKey")
    if err != nil {
        // Secret does not exist — create agent key in Meta OM and store it.
        projectConn := r.omConnectionFactory(&om.OMContext{
            BaseURL:    metaOM.CentralURL(),
            GroupID:    groupId,
            PublicKey:  publicKey,
            PrivateKey: privateKey,
        })
        newKey, err := projectConn.GenerateAgentKey()
        if err != nil {
            return construct.MetaOMEnvVars{}, workflow.Failed(fmt.Errorf("failed to generate agent API key in Meta OM: %w", err))
        }
        if err := secret.CreateOrUpdate(ctx, r.client, opsManager.Namespace, agentKeySecretName,
            map[string]string{"agentKey": newKey}); err != nil {
            return construct.MetaOMEnvVars{}, workflow.Failed(fmt.Errorf("failed to store agent key Secret: %w", err))
        }
        agentKey = newKey
        log.Infow("Created agent API key for Meta OM project", "project", ref.ProjectName)
    }

    return construct.MetaOMEnvVars{
        Server:  metaOM.CentralURL(),
        GroupID: groupId,
        APIKey:  agentKey,
    }, workflow.OK()
}
```

> **Note:** `MetaOMEnvVars` is a new small struct (see Task 4, Step 1 below). `conn.GetOrCreateProject(...)` may need to be added to the `Connection` interface or implemented as a local helper using `conn.CreateProject` + handling "already exists" errors — check `controllers/om/omclient.go` for the existing pattern used in `ReadOrCreateProject` in `controllers/operator/project/`.

### Step 4: Add `MetaOMEnvVars` helper struct to `appdb_construction.go`

To avoid an import cycle, define `MetaOMEnvVars` in `controllers/operator/construct/appdb_construction.go`:

```go
// MetaOMEnvVars holds the connection parameters needed to switch an AppDB agent
// from headless mode to online mode under a Meta OM instance.
type MetaOMEnvVars struct {
    Server  string
    GroupID string
    APIKey  string
}
```

Then update `AppDBStatefulSetOptions` to embed or accept this struct for clarity:

```go
// In AppDBStatefulSetOptions, replace the three separate fields with:
MetaOM MetaOMEnvVars
```

And update `appdbContainerEnv` to use `opts.MetaOM`:

```go
if opts.MetaOM.Server != "" && opts.MetaOM.GroupID != "" && opts.MetaOM.APIKey != "" {
    envVars = append(envVars,
        corev1.EnvVar{Name: "MMS_SERVER", Value: opts.MetaOM.Server},
        corev1.EnvVar{Name: "MMS_GROUP_ID", Value: opts.MetaOM.GroupID},
        corev1.EnvVar{Name: "MMS_API_KEY", Value: opts.MetaOM.APIKey},
    )
} else { ...headless... }
```

Also update the tests from Task 2 to use `AppDBStatefulSetOptions{MetaOM: construct.MetaOMEnvVars{...}}`.

### Step 5: Run the reconciler unit tests

```bash
go test ./controllers/operator/... -run "TestReconcileManagedByMetaOM" -v
```

Expected: `PASS` for all non-skipped tests. Remove `t.Skip(...)` from the tests you implemented.

### Step 6: Run all controller tests

```bash
go test ./controllers/operator/... -v 2>&1 | tail -30
```

Expected: all existing tests pass.

### Step 7: Commit

```bash
git add controllers/operator/appdbreplicaset_controller.go \
        controllers/operator/appdbreplicaset_controller_test.go \
        controllers/operator/construct/appdb_construction.go \
        controllers/operator/construct/appdb_construction_test.go
git commit -m "feat: implement reconcileManagedByMetaOM with idempotent Meta OM enrollment

Co-authored-by: Copilot <223556219+Copilot@users.noreply.github.com>"
```

---

## Task 4: Wire `reconcileManagedByMetaOM` into the Main Reconcile Loop

**Files:**
- Modify: `controllers/operator/appdbreplicaset_controller.go`
- Modify: `controllers/operator/appdbreplicaset_controller_test.go`

### Step 1: Write a failing integration-level unit test

```go
func TestReconcileAppDB_WithManagedByMetaOM_UpdatesStatefulSetEnvVars(t *testing.T) {
    ctx := context.Background()
    primaryOM := omv1.NewOpsManagerBuilderDefault().Build()
    primaryOM.Spec.AppDB.ManagedByMetaOM = &omv1.MetaOMRef{
        Name:        "om-meta",
        ProjectName: "primary-appdb",
        CredentialsSecretRef: corev1.LocalObjectReference{Name: "meta-om-creds"},
    }
    // ... set up meta OM, credentials secret, mock connection factory
    // Run ReconcileAppDB
    // Assert the AppDB StatefulSet has MMS_SERVER/MMS_GROUP_ID/MMS_API_KEY
    // and does NOT have HEADLESS_AGENT or AUTOMATION_CONFIG_MAP
    t.Skip("implement")
}
```

### Step 2: Run to confirm failure

```bash
go test ./controllers/operator/... -run "TestReconcileAppDB_WithManagedByMetaOM" -v
```

Expected: skip or fail.

### Step 3: Add the `managedByMetaOM` branch in `ReconcileAppDB`

Open `controllers/operator/appdbreplicaset_controller.go`, find `ReconcileAppDB` (line 521).

Early in the function — after the password is ensured and before the main `workflow.RunInGivenOrder` block — add:

```go
// If ManagedByMetaOM is configured, transition agents to online mode.
var metaOMEnvVars construct.MetaOMEnvVars
if opsManager.Spec.AppDB.ManagedByMetaOM != nil {
    var ws workflow.Status
    metaOMEnvVars, ws = r.reconcileManagedByMetaOM(ctx, opsManager, log)
    if !ws.IsOK() {
        return r.updateStatus(ctx, opsManager, ws, log)
    }
}
```

Then, where `AppDBStatefulSetOptions` is constructed (find the spot where `appdbOpts` is built and passed to `deployStatefulSet`), populate the Meta OM fields:

```go
appdbOpts := construct.AppDBStatefulSetOptions{
    // ... existing fields ...
    MetaOM: metaOMEnvVars,
}
```

> Find the exact variable name used for `AppDBStatefulSetOptions` in the existing reconcile code by searching for `AppDBStatefulSetOptions{` in `appdbreplicaset_controller.go`.

### Step 4: Run the test

```bash
go test ./controllers/operator/... -run "TestReconcileAppDB_WithManagedByMetaOM" -v
```

Expected: `PASS` (remove `t.Skip` first).

### Step 5: Run full test suite

```bash
go test ./... 2>&1 | grep -E "FAIL|ok" | head -40
```

Expected: no FAILs.

### Step 6: Commit

```bash
git add controllers/operator/appdbreplicaset_controller.go \
        controllers/operator/appdbreplicaset_controller_test.go
git commit -m "feat: wire reconcileManagedByMetaOM into AppDB reconcile loop

Co-authored-by: Copilot <223556219+Copilot@users.noreply.github.com>"
```

---

## Task 5: e2e Test

**Files:**
- Create: `test/e2e/opsmanager/appdb_meta_om_mode_switch_test.go` (adjust path to match existing e2e test conventions — look at neighbouring test files)

> Before writing: find an existing e2e test that deploys a `MongoDBOpsManager` and study its `TestMain`, helper functions, and how it asserts phases and pod states. Mirror that structure exactly.

### Step 1: Write the e2e test skeleton

```go
// +build e2e

package opsmanager

import (
    "testing"
    // ... mirror imports from neighbouring e2e tests
)

// TestAppDBMetaOMModeSwitchE2E validates the full headless → online agent transition.
// Scenario:
//   1. Deploy Primary OM + AppDB (headless mode) + sample MongoDB deployment
//   2. Deploy Meta OM + its own AppDB
//   3. Patch Primary OM with spec.applicationDatabase.managedByMetaOM
//   4. Assert AppDB pods restart and come back Ready
//   5. Assert env vars on AppDB pods reflect online mode
//   6. Assert sample MongoDB deployment is still healthy
func TestAppDBMetaOMModeSwitchE2E(t *testing.T) {
    t.Log("Step 1: Deploy Primary OM with headless AppDB")
    // deploy primaryOM CR, wait for Running phase
    // insert a document into AppDB to verify no data loss later

    t.Log("Step 2: Deploy a sample MongoDB managed by Primary OM")
    // deploy MongoDB CR, wait for Running phase

    t.Log("Step 3: Deploy Meta OM")
    // deploy metaOM CR, wait for Running phase

    t.Log("Step 4: Create Meta OM credentials Secret")
    // create Secret with publicKey/privateKey from Meta OM admin user

    t.Log("Step 5: Patch Primary OM to enable managedByMetaOM")
    // patch primaryOM CR, add spec.applicationDatabase.managedByMetaOM

    t.Log("Step 6: Wait for AppDB pods to restart and become Ready")
    // assert StatefulSet pods restart and all become Ready

    t.Log("Step 7: Assert env vars on AppDB pods")
    // exec into pod or read StatefulSet spec, assert MMS_SERVER present, HEADLESS_AGENT absent

    t.Log("Step 8: Assert no data loss")
    // query the document inserted in Step 1

    t.Log("Step 9: Assert sample MongoDB deployment still healthy")
    // assert MongoDB CR is still in Running phase
}
```

### Step 2: Run the e2e test against a local cluster

Follow the project's e2e test instructions (check `EVERGREEN.md` or `docs/` for how to run e2e tests locally with a Kind or real cluster).

```bash
go test ./test/e2e/opsmanager/... -run "TestAppDBMetaOMModeSwitchE2E" -v -tags e2e
```

Expected: all steps pass.

### Step 3: Commit

```bash
git add test/e2e/opsmanager/appdb_meta_om_mode_switch_test.go
git commit -m "test: add e2e test for AppDB headless to Meta OM online mode switch

Co-authored-by: Copilot <223556219+Copilot@users.noreply.github.com>"
```

---

## Final Verification

```bash
go build ./...
go test ./... 2>&1 | grep -E "FAIL|ok"
make generate  # ensure CRDs are up to date
```

Expected: clean build, no test failures, CRDs up to date.
