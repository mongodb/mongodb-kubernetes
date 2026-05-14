# Headless MongoDB Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `mode: Headless` option to the `MongoDB` CRD so MongoDB resources can run without an Ops Manager connection, and add in-place migration from headless to online (OpsManager/CloudManager) mode.

**Architecture:** A `mode` field is added to the inlined `ConnectionSpec` in `DbCommonSpec`. The existing MongoDB reconciler (`mongodbreplicaset_controller.go`) dispatches to `reconcileHeadless` when `mode == Headless`, which writes an automation config to a Kubernetes Secret and builds the StatefulSet with `HEADLESS_AGENT=true`. Migration is detected by the reconciler when `spec.mode` changes to online while the StatefulSet still carries `HEADLESS_AGENT=true`; the controller then calculates a fresh AC using the existing builder, pushes it to OM, and rolls the pods.

**Tech Stack:** Go, controller-runtime, kubebuilder, `pkg/automationconfig` Builder, `automationconfig.EnsureSecret`, `mongodbreplicaset_controller.go`, `api/v1/mdb/mongodb_types.go`

**Spec:** `docs/superpowers/specs/2026-05-14-headless-mongodb-design.md`

---

## File Map

| Action | File | What changes |
|--------|------|--------------|
| Modify | `api/v1/mdb/mongodb_types.go` | Add `ConnectionMode` type + constants; add `Mode` field to `ConnectionSpec`; change `Credentials` to `omitempty`; add `IsHeadless()`; update `GetConnectionSpec()` |
| Modify | `api/v1/mdb/mongodb_validation.go` | Gate `specWithExactlyOneSchema` on `!IsHeadless()`; add `specHeadlessHasNoCredentials` validator; register it |
| Modify | `api/v1/mdb/mongodb_validation_test.go` | Add headless validation tests |
| Create | `controllers/operator/construct/headless_agent_command.go` | Headless agent command builder + env vars + volumes for MongoDB headless mode |
| Create | `controllers/operator/construct/headless_agent_command_test.go` | Tests for the above |
| Modify | `controllers/operator/mongodbreplicaset_controller.go` | Add mode dispatch in `Reconcile`; implement `reconcileHeadless`; implement `buildHeadlessAutomationConfig`; implement migration steps |
| Modify | `controllers/operator/mongodbreplicaset_controller_test.go` | Add `SetMode` to `ReplicaSetBuilder`; add `checkHeadlessReconcileSuccessful`; add all new tests |
| Regen | `config/crd/bases/mongodb.com_mongodb.yaml` | `make generate` — remove `credentials` from `required`; add `mode` enum |

---

## Task 1: Add `ConnectionMode` to API types

**Files:**
- Modify: `api/v1/mdb/mongodb_types.go`
- Test: `api/v1/mdb/mongodb_types_test.go` (create if absent)

- [ ] **Step 1: Write failing tests**

Add to `api/v1/mdb/mongodb_types_test.go`:

```go
package mdb_test

import (
    "testing"
    "github.com/stretchr/testify/assert"
    mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
)

func TestIsHeadless_WhenModeIsHeadless(t *testing.T) {
    spec := mdbv1.ConnectionSpec{Mode: mdbv1.ConnectionModeHeadless}
    assert.True(t, spec.IsHeadless())
}

func TestIsHeadless_WhenModeIsOpsManager(t *testing.T) {
    spec := mdbv1.ConnectionSpec{Mode: mdbv1.ConnectionModeOpsManager, Credentials: "creds"}
    assert.False(t, spec.IsHeadless())
}

func TestIsHeadless_WhenModeIsEmpty_DefaultsToOpsManager(t *testing.T) {
    spec := mdbv1.ConnectionSpec{Credentials: "creds"}
    assert.False(t, spec.IsHeadless())
}

func TestGetConnectionSpec_ReturnsNilWhenHeadless(t *testing.T) {
    mdb := &mdbv1.MongoDB{}
    mdb.Spec.Mode = mdbv1.ConnectionModeHeadless
    assert.Nil(t, mdb.GetConnectionSpec())
}

func TestGetConnectionSpec_ReturnsSpecWhenOnline(t *testing.T) {
    mdb := &mdbv1.MongoDB{}
    mdb.Spec.Mode = mdbv1.ConnectionModeOpsManager
    mdb.Spec.Credentials = "creds"
    assert.NotNil(t, mdb.GetConnectionSpec())
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
cd /Users/maciej.karas/mongodb/mongodb-kubernetes
go test ./api/v1/mdb/... -run "TestIsHeadless|TestGetConnectionSpec" -v 2>&1 | tail -20
```

Expected: `FAIL` — `ConnectionModeHeadless undefined`, `IsHeadless undefined`.

- [ ] **Step 3: Add `AutomationConfigSecretName()` to `MongoDB`**

AppDB has this method (`api/v1/om/appdb_types.go:406`); MongoDB does not. Add it to `api/v1/mdb/mongodb_types.go`:

```go
func (m *MongoDB) AutomationConfigSecretName() string {
    return m.Name + "-config"
}
```

- [ ] **Step 3b: Add `ConnectionMode` type and update `ConnectionSpec`**

In `api/v1/mdb/mongodb_types.go`, find the `ConnectionSpec` struct (around line 735) and the block before it. Add the type and update the struct:

```go
// ConnectionMode controls whether this MongoDB resource connects to Ops Manager
// or runs in headless (agent-only, no OM) mode.
// +kubebuilder:validation:Enum=OpsManager;CloudManager;Headless
type ConnectionMode string

const (
    ConnectionModeOpsManager   ConnectionMode = "OpsManager"
    ConnectionModeCloudManager ConnectionMode = "CloudManager"
    ConnectionModeHeadless     ConnectionMode = "Headless"
)

type ConnectionSpec struct {
    SharedConnectionSpec `json:",inline"`
    // Name of the Secret holding credentials information.
    // Required when mode is OpsManager or CloudManager; must be absent when mode is Headless.
    // +optional
    Credentials string `json:"credentials,omitempty"`
    // Mode controls whether agents connect to Ops Manager or run headlessly.
    // Defaults to OpsManager for backward compatibility.
    // +optional
    // +kubebuilder:default=OpsManager
    Mode ConnectionMode `json:"mode,omitempty"`
}
```

Remove the `// +kubebuilder:validation:Required` marker that was above `Credentials`.

- [ ] **Step 4: Add `IsHeadless()` helper on `ConnectionSpec`**

```go
func (c *ConnectionSpec) IsHeadless() bool {
    return c.Mode == ConnectionModeHeadless
}
```

- [ ] **Step 5: Update `MongoDB.GetConnectionSpec()` to return nil for headless**

Find `GetConnectionSpec()` (around line 141) and update:

```go
func (m *MongoDB) GetConnectionSpec() *ConnectionSpec {
    if m.Spec.ConnectionSpec.IsHeadless() {
        return nil
    }
    return &m.Spec.ConnectionSpec
}
```

- [ ] **Step 6: Run tests to verify they pass**

```bash
go test ./api/v1/mdb/... -run "TestIsHeadless|TestGetConnectionSpec" -v 2>&1 | tail -20
```

Expected: all PASS.

- [ ] **Step 7: Commit**

```bash
git add api/v1/mdb/mongodb_types.go api/v1/mdb/mongodb_types_test.go
git commit -m "feat(headless): add ConnectionMode type and mode field to ConnectionSpec"
```

---

## Task 2: Update webhook validation for headless mode

**Files:**
- Modify: `api/v1/mdb/mongodb_validation.go`
- Modify: `api/v1/mdb/mongodb_validation_test.go`

- [ ] **Step 1: Write failing tests**

Add to `api/v1/mdb/mongodb_validation_test.go`:

```go
func TestHeadlessMode_NoCredentials_Passes(t *testing.T) {
    rs := NewReplicaSetBuilder().Build()
    rs.Spec.Mode = mdbv1.ConnectionModeHeadless
    rs.Spec.Credentials = ""
    rs.Spec.OpsManagerConfig = &mdbv1.PrivateCloudConfig{}
    rs.Spec.CloudManagerConfig = &mdbv1.PrivateCloudConfig{}
    err := rs.ProcessValidationsOnReconcile(nil)
    assert.NoError(t, err)
}

func TestHeadlessMode_WithCredentials_Fails(t *testing.T) {
    rs := NewReplicaSetBuilder().Build()
    rs.Spec.Mode = mdbv1.ConnectionModeHeadless
    rs.Spec.Credentials = "my-creds"
    rs.Spec.OpsManagerConfig = &mdbv1.PrivateCloudConfig{}
    rs.Spec.CloudManagerConfig = &mdbv1.PrivateCloudConfig{}
    err := rs.ProcessValidationsOnReconcile(nil)
    assert.EqualError(t, err, "credentials must not be set when mode is Headless")
}

func TestHeadlessMode_WithOpsManagerConfig_Fails(t *testing.T) {
    rs := NewReplicaSetBuilder().Build()
    rs.Spec.Mode = mdbv1.ConnectionModeHeadless
    rs.Spec.Credentials = ""
    rs.Spec.OpsManagerConfig = &mdbv1.PrivateCloudConfig{ConfigMapRef: mdbv1.ConfigMapRef{Name: "my-cm"}}
    rs.Spec.CloudManagerConfig = &mdbv1.PrivateCloudConfig{}
    err := rs.ProcessValidationsOnReconcile(nil)
    assert.EqualError(t, err, "opsManager and cloudManager must not be set when mode is Headless")
}

func TestOnlineMode_WithoutCredentials_Fails(t *testing.T) {
    rs := NewReplicaSetBuilder().Build()
    rs.Spec.Mode = mdbv1.ConnectionModeOpsManager
    rs.Spec.Credentials = ""
    rs.Spec.OpsManagerConfig = &mdbv1.PrivateCloudConfig{ConfigMapRef: mdbv1.ConfigMapRef{Name: "my-cm"}}
    rs.Spec.CloudManagerConfig = &mdbv1.PrivateCloudConfig{}
    err := rs.ProcessValidationsOnReconcile(nil)
    assert.Contains(t, err.Error(), "credentials")
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./api/v1/mdb/... -run "TestHeadlessMode|TestOnlineMode_WithoutCredentials" -v 2>&1 | tail -20
```

Expected: FAIL.

- [ ] **Step 3: Gate `specWithExactlyOneSchema` on non-headless mode**

In `api/v1/mdb/mongodb_validation.go`, update `specWithExactlyOneSchema`:

```go
func specWithExactlyOneSchema(d DbCommonSpec) v1.ValidationResult {
    if d.ConnectionSpec.IsHeadless() {
        return v1.ValidationSuccess()
    }
    count := 0
    if *d.OpsManagerConfig != (PrivateCloudConfig{}) {
        count += 1
    }
    if *d.CloudManagerConfig != (PrivateCloudConfig{}) {
        count += 1
    }
    if count != 1 {
        return v1.ValidationError("either spec.cloudManager or spec.opsManager can be set")
    }
    return v1.ValidationSuccess()
}
```

- [ ] **Step 4: Add `specHeadlessHasNoCredentials` validator**

Add a new validator function after `specWithExactlyOneSchema`:

```go
func specHeadlessHasNoCredentials(d DbCommonSpec) v1.ValidationResult {
    if !d.ConnectionSpec.IsHeadless() {
        return v1.ValidationSuccess()
    }
    if d.Credentials != "" {
        return v1.ValidationError("credentials must not be set when mode is Headless")
    }
    if *d.OpsManagerConfig != (PrivateCloudConfig{}) || *d.CloudManagerConfig != (PrivateCloudConfig{}) {
        return v1.ValidationError("opsManager and cloudManager must not be set when mode is Headless")
    }
    return v1.ValidationSuccess()
}
```

- [ ] **Step 5: Register the new validator in `CommonValidators()`**

In `CommonValidators()`, add `specHeadlessHasNoCredentials` to the validators slice:

```go
func CommonValidators(db DbCommonSpec) []func(d DbCommonSpec) v1.ValidationResult {
    validators := []func(d DbCommonSpec) v1.ValidationResult{
        // ... existing validators ...
        specWithExactlyOneSchema,
        specHeadlessHasNoCredentials,  // add this line
        featureCompatibilityVersionValidation,
    }
    // ...
}
```

- [ ] **Step 6: Run tests to verify they pass**

```bash
go test ./api/v1/mdb/... -run "TestHeadlessMode|TestOnlineMode_WithoutCredentials" -v 2>&1 | tail -20
```

Expected: all PASS.

- [ ] **Step 7: Run full validation test suite to check for regressions**

```bash
go test ./api/v1/mdb/... -v 2>&1 | grep -E "PASS|FAIL|---" | tail -40
```

Expected: all PASS.

- [ ] **Step 8: Commit**

```bash
git add api/v1/mdb/mongodb_validation.go api/v1/mdb/mongodb_validation_test.go
git commit -m "feat(headless): update webhook validation — gate OM validators on non-headless mode"
```

---

## Task 3: Headless agent command builder

The existing agent setup in `database_construction.go` uses `agent-launcher-shim.sh` and passes OM connectivity via env vars (`BASE_URL`, `GROUP_ID`). For headless, agents must use `-cluster=<path>` directly (same pattern as AppDB — see `controllers/operator/construct/appdb_agent_command.go`).

**Files:**
- Create: `controllers/operator/construct/headless_agent_command.go`
- Create: `controllers/operator/construct/headless_agent_command_test.go`

- [ ] **Step 1: Write failing tests**

Create `controllers/operator/construct/headless_agent_command_test.go`:

```go
package construct_test

import (
    "testing"
    "github.com/stretchr/testify/assert"
    corev1 "k8s.io/api/core/v1"
    "github.com/mongodb/mongodb-kubernetes/controllers/operator/construct"
)

func TestHeadlessAgentCommand_ContainsClusterFlag(t *testing.T) {
    cmd := construct.HeadlessAutomationAgentCommand("info", "/dev/stdout", 24)
    assert.Contains(t, cmd[len(cmd)-1], "-cluster="+construct.HeadlessClusterFilePath)
    assert.NotContains(t, cmd[len(cmd)-1], "-mmsBaseUrl")
}

func TestHeadlessAgentEnvVars_ContainsHeadlessFlag(t *testing.T) {
    envs := construct.HeadlessAgentEnvVars("my-config-secret")
    names := make([]string, len(envs))
    for i, e := range envs {
        names[i] = e.Name
    }
    assert.Contains(t, names, "HEADLESS_AGENT")
    assert.Contains(t, names, "AUTOMATION_CONFIG_MAP")
    assert.NotContains(t, names, "BASE_URL")
    assert.NotContains(t, names, "GROUP_ID")
}

func TestHeadlessAgentEnvVars_HeadlessAgentIsTrue(t *testing.T) {
    envs := construct.HeadlessAgentEnvVars("my-config-secret")
    for _, e := range envs {
        if e.Name == "HEADLESS_AGENT" {
            assert.Equal(t, "true", e.Value)
            return
        }
    }
    t.Fatal("HEADLESS_AGENT env var not found")
}

func TestHeadlessAgentEnvVars_AutomationConfigMapSecretName(t *testing.T) {
    envs := construct.HeadlessAgentEnvVars("my-config-secret")
    for _, e := range envs {
        if e.Name == "AUTOMATION_CONFIG_MAP" {
            assert.Equal(t, "my-config-secret", e.Value)
            return
        }
    }
    t.Fatal("AUTOMATION_CONFIG_MAP env var not found")
}

func TestAgentDownloadsVolume_IsEmptyDir(t *testing.T) {
    vol := construct.AgentDownloadsVolume()
    assert.Equal(t, "agent-downloads", vol.Name)
    assert.NotNil(t, vol.EmptyDir)
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./controllers/operator/construct/... -run "TestHeadlessAgent|TestAgentDownloads" -v 2>&1 | tail -20
```

Expected: FAIL — `construct.HeadlessAutomationAgentCommand undefined`.

- [ ] **Step 3: Implement `headless_agent_command.go`**

Create `controllers/operator/construct/headless_agent_command.go`:

```go
package construct

import (
    "strconv"
    corev1 "k8s.io/api/core/v1"
    v1 "github.com/mongodb/mongodb-kubernetes/api/v1"
)

const (
    // HeadlessClusterFilePath is the path inside the agent container where
    // the automation config Secret is mounted.
    HeadlessClusterFilePath = "/var/lib/automation/config/cluster-config.json"

    headlessAgentHealthStatusFilePath = "/var/log/mongodb-mms-automation/healthstatus/agent-health-status.json"
    headlessAgentOptions              = " -skipMongoStart -noDaemonize -useLocalMongoDbTools"
    headlessAgentEnvName              = "HEADLESS_AGENT"
    headlessAutomationConfigMapEnv    = "AUTOMATION_CONFIG_MAP"
    headlessAgentDownloadsVolumeName  = "agent-downloads"
)

// HeadlessBaseAgentCommand returns the core agent binary invocation for headless mode.
func HeadlessBaseAgentCommand() string {
    return "agent/mongodb-agent -healthCheckFilePath=" + headlessAgentHealthStatusFilePath + " -serveStatusPort=5000"
}

// HeadlessAutomationAgentCommand returns the full command for the automation agent
// container in headless mode. Agents read from a local cluster-config.json Secret
// mount instead of connecting to Ops Manager.
func HeadlessAutomationAgentCommand(logLevel v1.LogLevel, logFile string, maxLogFileDurationHours int) []string {
    logOpts := ""
    if logFile == "/dev/stdout" {
        logOpts = " -logLevel " + string(logLevel)
    } else {
        logOpts = " -logFile " + logFile +
            " -logLevel " + string(logLevel) +
            " -maxLogFileDurationHrs " + strconv.Itoa(maxLogFileDurationHours)
    }
    cmd := MongodbUserCommand + HeadlessBaseAgentCommand() +
        " -cluster=" + HeadlessClusterFilePath + headlessAgentOptions + logOpts
    return []string{"/bin/bash", "-c", cmd}
}

// HeadlessAgentEnvVars returns the env vars that put an agent container into headless mode.
// configSecretName is the name of the Secret holding cluster-config.json.
func HeadlessAgentEnvVars(configSecretName string) []corev1.EnvVar {
    return []corev1.EnvVar{
        {Name: headlessAgentEnvName, Value: "true"},
        {Name: headlessAutomationConfigMapEnv, Value: configSecretName},
    }
}

// AgentDownloadsVolume returns an emptyDir volume required by the agent for caching
// downloaded binaries. Present in both headless and online modes so that migrating
// headless → online does not require a pod restart solely for volume addition.
func AgentDownloadsVolume() corev1.Volume {
    return corev1.Volume{
        Name:         headlessAgentDownloadsVolumeName,
        VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
    }
}
```

Note: `MongodbUserCommand` is already defined in `appdb_agent_command.go` in the same package — reuse it.

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./controllers/operator/construct/... -run "TestHeadlessAgent|TestAgentDownloads" -v 2>&1 | tail -20
```

Expected: all PASS.

- [ ] **Step 5: Run full construct package tests for regressions**

```bash
go test ./controllers/operator/construct/... -v 2>&1 | grep -E "PASS|FAIL|---" | tail -30
```

Expected: all PASS.

- [ ] **Step 6: Commit**

```bash
git add controllers/operator/construct/headless_agent_command.go \
        controllers/operator/construct/headless_agent_command_test.go
git commit -m "feat(headless): add HeadlessAutomationAgentCommand and HeadlessAgentEnvVars builders"
```

---

## Task 4: `reconcileHeadless` and headless automation config

Implement the main headless reconcile path in the MongoDB controller.

**Files:**
- Modify: `controllers/operator/mongodbreplicaset_controller.go`
- Modify: `controllers/operator/mongodbreplicaset_controller_test.go`

**Background on automation config:** For headless mode, the AC is built using `automationconfig.Builder` (same package used by AppDB — see `buildAppDbAutomationConfig` in `appdbreplicaset_controller.go` around line 1085 as the reference implementation). The built AC is written to a Kubernetes Secret named `<rs.Name>-config` using `automationconfig.EnsureSecret` (defined in `pkg/automationconfig/automation_config_secret.go:33`).

- [ ] **Step 1: Add `SetMode` to `ReplicaSetBuilder` and write failing controller test**

In `controllers/operator/mongodbreplicaset_controller_test.go`, add `SetMode` to `ReplicaSetBuilder`:

```go
func (b *ReplicaSetBuilder) SetMode(mode mdbv1.ConnectionMode) *ReplicaSetBuilder {
    b.Spec.Mode = mode
    return b
}
```

Add a `checkHeadlessReconcileSuccessful` helper below `checkReconcileSuccessful`:

```go
func checkHeadlessReconcileSuccessful(ctx context.Context, t *testing.T, reconciler reconcile.Reconciler, rs *mdbv1.MongoDB, client client.Client) {
    t.Helper()
    err := client.Update(ctx, rs)
    require.NoError(t, err)
    res, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: rs.NamespacedName()})
    require.NoError(t, err)
    assert.Equal(t, reconcile.Result{RequeueAfter: util.TWENTY_FOUR_HOURS}, res)
    updatedRS := &mdbv1.MongoDB{}
    err = client.Get(ctx, rs.NamespacedName(), updatedRS)
    require.NoError(t, err)
    assert.Equal(t, status.Running, updatedRS.Status.Phase)
}
```

Add the test:

```go
func TestReconcileHeadlessReplicaSet(t *testing.T) {
    ctx := context.Background()
    rs := DefaultReplicaSetBuilder().
        SetMode(mdbv1.ConnectionModeHeadless).
        Build()
    rs.Spec.Credentials = ""
    rs.Spec.OpsManagerConfig = &mdbv1.PrivateCloudConfig{}
    rs.Spec.CloudManagerConfig = &mdbv1.PrivateCloudConfig{}

    reconciler, client, _ := defaultReplicaSetReconciler(ctx, nil, "", "", rs)

    checkHeadlessReconcileSuccessful(ctx, t, reconciler, rs, client)

    // StatefulSet must be created
    sts, err := client.GetStatefulSet(ctx, rs.ObjectKey())
    require.NoError(t, err)
    assert.Equal(t, int32(3), *sts.Spec.Replicas)

    // Agent container must have HEADLESS_AGENT=true
    agentContainer := getContainerByName(sts.Spec.Template.Spec.Containers, util.AgentContainerName)
    require.NotNil(t, agentContainer)
    envNames := make([]string, len(agentContainer.Env))
    for i, e := range agentContainer.Env {
        envNames[i] = e.Name
    }
    assert.Contains(t, envNames, "HEADLESS_AGENT")
    assert.NotContains(t, envNames, "BASE_URL")

    // Agent command must use -cluster=
    assert.Contains(t, strings.Join(agentContainer.Command, " ")+strings.Join(agentContainer.Args, " "),
        "-cluster=")

    // AC Secret must be created
    acSecret := &corev1.Secret{}
    err = client.Get(ctx, types.NamespacedName{Name: rs.Name + "-config", Namespace: rs.Namespace}, acSecret)
    require.NoError(t, err)
    assert.NotEmpty(t, acSecret.Data)
}

func TestReconcileHeadlessShardedCluster_ReturnsError(t *testing.T) {
    ctx := context.Background()
    rs := DefaultReplicaSetBuilder().
        SetMode(mdbv1.ConnectionModeHeadless).
        Build()
    rs.Spec.ResourceType = mdbv1.ShardedCluster
    rs.Spec.Credentials = ""
    rs.Spec.OpsManagerConfig = &mdbv1.PrivateCloudConfig{}
    rs.Spec.CloudManagerConfig = &mdbv1.PrivateCloudConfig{}

    reconciler, client, _ := defaultReplicaSetReconciler(ctx, nil, "", "", rs)
    err := client.Update(ctx, rs)
    require.NoError(t, err)

    _, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: rs.NamespacedName()})
    require.NoError(t, err) // reconciler does not return Go errors for validation failures

    updatedRS := &mdbv1.MongoDB{}
    require.NoError(t, client.Get(ctx, rs.NamespacedName(), updatedRS))
    assert.Equal(t, status.Failed, updatedRS.Status.Phase)
    assert.Contains(t, updatedRS.Status.Message, "headless mode does not support sharded clusters")
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./controllers/operator/... -run "TestReconcileHeadless" -v 2>&1 | tail -20
```

Expected: FAIL — `reconcileHeadless undefined`.

- [ ] **Step 3: Add mode dispatch in `Reconcile`**

In `controllers/operator/mongodbreplicaset_controller.go`, in the `Reconcile` function, after `prepareResourceForReconciliation` and before `r.newReconcilerHelper(ctx, rs, log)`:

```go
func (r *ReconcileMongoDbReplicaSet) Reconcile(ctx context.Context, request reconcile.Request) (res reconcile.Result, e error) {
    log := zap.S().With("ReplicaSet", request.NamespacedName)
    rs := &mdbv1.MongoDB{}

    if reconcileResult, err := r.prepareResourceForReconciliation(ctx, request, rs, log); err != nil {
        if errors.IsNotFound(err) {
            return workflow.Invalid("Object for reconciliation not found").ReconcileResult()
        }
        return reconcileResult, err
    }

    // Headless mode bypasses the OM-connected reconcile path entirely.
    if rs.Spec.ConnectionSpec.IsHeadless() {
        return r.reconcileHeadless(ctx, rs, log)
    }

    helper, err := r.newReconcilerHelper(ctx, rs, log)
    // ... rest unchanged
```

- [ ] **Step 4: Implement `reconcileHeadless`**

Add to `controllers/operator/mongodbreplicaset_controller.go`:

```go
// reconcileHeadless reconciles a MongoDB resource in headless mode (no Ops Manager connection).
// Agents are configured via a local automation config Secret instead of the OM API.
func (r *ReconcileMongoDbReplicaSet) reconcileHeadless(ctx context.Context, rs *mdbv1.MongoDB, log *zap.SugaredLogger) (reconcile.Result, error) {
    if rs.Spec.ResourceType == mdbv1.ShardedCluster {
        return r.updateStatus(ctx, rs, workflow.Invalid("headless mode does not support sharded clusters"), log)
    }

    // Build and persist the automation config Secret.
    ac, err := r.buildHeadlessAutomationConfig(ctx, rs, log)
    if err != nil {
        return r.updateStatus(ctx, rs, workflow.Failed(err), log)
    }
    secretNsName := types.NamespacedName{Name: rs.AutomationConfigSecretName(), Namespace: rs.Namespace}
    if _, err = automationconfig.EnsureSecret(ctx, r.SecretClient, secretNsName, rs.GetOwnerReferences(), ac); err != nil {
        return r.updateStatus(ctx, rs, workflow.Failed(err), log)
    }

    // Build StatefulSet with headless-specific modifications.
    if err = r.reconcileHeadlessStatefulSet(ctx, rs, log); err != nil {
        return r.updateStatus(ctx, rs, workflow.Failed(err), log)
    }

    // Headless readiness: use StatefulSet.IsReady() (agents do not write pod annotations in headless mode).
    ready, err := statefulset.IsReady(ctx, r.client, rs.ObjectKey(), rs.Spec.Members)
    if err != nil {
        return r.updateStatus(ctx, rs, workflow.Failed(err), log)
    }
    if !ready {
        return r.updateStatus(ctx, rs, workflow.Pending("waiting for StatefulSet to be ready"), log)
    }

    return r.updateStatus(ctx, rs, workflow.Running(), log)
}
```

- [ ] **Step 5: Implement `buildHeadlessAutomationConfig`**

This function builds the automation config using `automationconfig.Builder`. Reference: `buildAppDbAutomationConfig` in `appdbreplicaset_controller.go:1085`. Key differences from AppDB: use `rs.Spec.Members`, `rs.Spec.Version`, and MongoDB-specific member config.

```go
func (r *ReconcileMongoDbReplicaSet) buildHeadlessAutomationConfig(ctx context.Context, rs *mdbv1.MongoDB, log *zap.SugaredLogger) (automationconfig.AutomationConfig, error) {
    domain := rs.ServiceName() + "." + rs.Namespace + ".svc." + rs.Spec.GetClusterDomain()

    auth := automationconfig.Auth{}
    if err := scram.Enable(ctx, &auth, r.SecretClient, rs); err != nil {
        return automationconfig.AutomationConfig{}, err
    }

    // Load existing AC for version tracking (builder increments version if config changed).
    existingAC, err := r.getExistingHeadlessAutomationConfig(ctx, rs)
    if err != nil {
        return automationconfig.AutomationConfig{}, err
    }

    // TLS CA path: use the Security spec (rs.Spec.GetSecurity() returns *Security;
    // see GetTLSConfig() around line 1164 of mongodb_types.go for the pattern).
    var caFilePath string
    if rs.Spec.GetSecurity().IsTLSEnabled() {
        caFilePath = rs.Spec.GetSecurity().GetTLSConfig().CA
    }

    builder := automationconfig.NewBuilder().
        SetName(rs.Name).
        SetDomain(domain).
        SetMembers(rs.Spec.Members).
        SetMemberOptions(rs.Spec.MemberConfig).
        SetMongoDBVersion(rs.Spec.Version).
        SetCAFilePath(caFilePath).
        SetAuth(auth).
        SetPreviousAutomationConfig(existingAC)

    if fcv := rs.Spec.FeatureCompatibilityVersion; fcv != nil {
        builder.SetFCV(*fcv)
    }

    if rs.Spec.GetSecurity().IsTLSEnabled() {
        tlsCfg := automationconfig.TLS{
            CAFilePath: caFilePath,
        }
        builder.SetTLSConfig(tlsCfg)
    }

    return builder.Build()
}

func (r *ReconcileMongoDbReplicaSet) getExistingHeadlessAutomationConfig(ctx context.Context, rs *mdbv1.MongoDB) (automationconfig.AutomationConfig, error) {
    secretNsName := types.NamespacedName{Name: rs.AutomationConfigSecretName(), Namespace: rs.Namespace}
    return automationconfig.ReadFromSecret(ctx, r.SecretClient, secretNsName)
}
```

`automationconfig.ReadFromSecret` exists at `pkg/automationconfig/automation_config_secret.go:16`.

- [ ] **Step 6: Implement `reconcileHeadlessStatefulSet`**

This builds the StatefulSet using the existing builder but applies headless-specific container modifications:

```go
func (r *ReconcileMongoDbReplicaSet) reconcileHeadlessStatefulSet(ctx context.Context, rs *mdbv1.MongoDB, log *zap.SugaredLogger) error {
    secretName := rs.AutomationConfigSecretName()

    headlessMod := func(sts *appsv1.StatefulSet) {
        for i, c := range sts.Spec.Template.Spec.Containers {
            if c.Name != util.AgentContainerName {
                continue
            }
            // Replace OM-based env vars with headless env vars.
            headlessEnvs := construct.HeadlessAgentEnvVars(secretName)
            filtered := make([]corev1.EnvVar, 0, len(c.Env))
            omEnvNames := map[string]bool{"BASE_URL": true, "GROUP_ID": true, "USER_LOGIN": true}
            for _, e := range c.Env {
                if !omEnvNames[e.Name] {
                    filtered = append(filtered, e)
                }
            }
            sts.Spec.Template.Spec.Containers[i].Env = append(filtered, headlessEnvs...)

            // Override agent command to use -cluster= instead of OM connectivity.
            agentCfg := rs.Spec.Agent
            sts.Spec.Template.Spec.Containers[i].Command = construct.HeadlessAutomationAgentCommand(
                agentCfg.LogLevel,
                agentCfg.GetLogFile(),
                agentCfg.MaxLogFileDurationHours,
            )
        }
        // Add agent-downloads emptyDir (needed for future headless→online migration).
        sts.Spec.Template.Spec.Volumes = append(sts.Spec.Template.Spec.Volumes, construct.AgentDownloadsVolume())
    }

    return r.reconcileStatefulSet(ctx, rs, headlessMod, log)
}
```

Note: `reconcileStatefulSet` is the existing function that builds and applies the StatefulSet; the `headlessMod` function is passed as a `StatefulSetModification`. Adjust the call signature to match the actual function signature in the codebase.

- [ ] **Step 7: Add multi-cluster headless test**

The `MongoDB` CRD supports multi-cluster via `spec.topology: MultiCluster` + `clusterSpecList`. The existing StatefulSet builder distributes StatefulSets to member clusters; `reconcileHeadless` must not break this.

```go
func TestReconcileHeadlessMultiCluster(t *testing.T) {
    ctx := context.Background()
    rs := DefaultReplicaSetBuilder().
        SetMode(mdbv1.ConnectionModeHeadless).
        Build()
    rs.Spec.Topology = mdbv1.TopologyMultiCluster
    rs.Spec.ClusterSpecList = mdbmultiv1.ClusterSpecList{
        {ClusterName: "cluster1", Members: 2},
        {ClusterName: "cluster2", Members: 1},
    }
    rs.Spec.Credentials = ""
    rs.Spec.OpsManagerConfig = &mdbv1.PrivateCloudConfig{}
    rs.Spec.CloudManagerConfig = &mdbv1.PrivateCloudConfig{}

    reconciler, client, _ := defaultReplicaSetReconciler(ctx, nil, "", "", rs)
    checkHeadlessReconcileSuccessful(ctx, t, reconciler, rs, client)

    // AC Secret must exist in the central cluster.
    acSecret := &corev1.Secret{}
    err := client.Get(ctx, types.NamespacedName{Name: rs.Name + "-config", Namespace: rs.Namespace}, acSecret)
    require.NoError(t, err)
    assert.NotEmpty(t, acSecret.Data)
}
```

- [ ] **Step 8: Run tests to verify they pass**

```bash
go test ./controllers/operator/... -run "TestReconcileHeadless" -v 2>&1 | tail -30
```

Expected: all PASS.

- [ ] **Step 9: Commit**

```bash
git add controllers/operator/mongodbreplicaset_controller.go \
        controllers/operator/mongodbreplicaset_controller_test.go
git commit -m "feat(headless): implement reconcileHeadless and buildHeadlessAutomationConfig"
```

---

## Task 5: Headless → Online migration

When a user patches `spec.mode` from `Headless` to `OpsManager` or `CloudManager` (and adds credentials + opsManager/cloudManager), the controller must: calculate a fresh AC using the existing MongoDB online AC builder, push it to OM, create an agent key, and update the StatefulSet.

**Files:**
- Modify: `controllers/operator/mongodbreplicaset_controller.go`
- Modify: `controllers/operator/mongodbreplicaset_controller_test.go`

- [ ] **Step 1: Write failing tests**

```go
func TestMigrationDetected_WhenStatefulSetHasHeadlessEnvAndModeIsOnline(t *testing.T) {
    ctx := context.Background()
    // Start with a headless RS that has already been reconciled (has HEADLESS_AGENT in sts).
    rs := DefaultReplicaSetBuilder().
        SetMode(mdbv1.ConnectionModeHeadless).
        Build()
    rs.Spec.Credentials = ""
    rs.Spec.OpsManagerConfig = &mdbv1.PrivateCloudConfig{}
    rs.Spec.CloudManagerConfig = &mdbv1.PrivateCloudConfig{}

    reconciler, client, _ := defaultReplicaSetReconciler(ctx, nil, "", "", rs)
    checkHeadlessReconcileSuccessful(ctx, t, reconciler, rs, client)

    // Now simulate the user patching to online mode.
    rs.Spec.Mode = mdbv1.ConnectionModeOpsManager
    rs.Spec.Credentials = "my-creds"
    rs.Spec.OpsManagerConfig = &mdbv1.PrivateCloudConfig{ConfigMapRef: mdbv1.ConfigMapRef{Name: "my-om-cm"}}

    // Patch to online and trigger reconcile — the reconciler should detect migration.
    rs.Spec.Mode = mdbv1.ConnectionModeOpsManager
    rs.Spec.Credentials = "my-creds"
    rs.Spec.OpsManagerConfig = &mdbv1.PrivateCloudConfig{ConfigMapRef: mdbv1.ConfigMapRef{Name: "my-om-cm"}}
    err = client.Update(ctx, rs)
    require.NoError(t, err)

    res, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: rs.NamespacedName()})
    require.NoError(t, err)

    // During migration pods are rolling — expect Pending, not Running yet.
    updatedRS := &mdbv1.MongoDB{}
    require.NoError(t, client.Get(ctx, rs.NamespacedName(), updatedRS))
    assert.Equal(t, status.Pending, updatedRS.Status.Phase)
    assert.Contains(t, updatedRS.Status.Message, "migration")
    _ = res
}

func TestMigrationNotNeeded_WhenStatefulSetAlreadyOnline(t *testing.T) {
    ctx := context.Background()
    rs := DefaultReplicaSetBuilder().Build() // online from the start
    reconciler, client, omConnectionFactory := defaultReplicaSetReconciler(ctx, nil, "", "", rs)
    checkReconcileSuccessful(ctx, t, reconciler, rs, client)

    // A second reconcile on an already-online resource must not trigger migration.
    checkReconcileSuccessful(ctx, t, reconciler, rs, client)
    conn := omConnectionFactory.GetConnection()
    conn.(*om.MockedOmConnection).CheckNumberOfUpdateRequests(t, 2) // one per reconcile, no extra
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./controllers/operator/... -run "TestMigration" -v 2>&1 | tail -20
```

Expected: FAIL — `isMigratingFromHeadless undefined`.

- [ ] **Step 3: Implement migration detection**

The codebase pattern (see `ReplicaSetReconcilerHelper` methods in `mongodbreplicaset_controller.go`) is to put helpers as methods on the reconciler, not package-level functions. Add to `controllers/operator/mongodbreplicaset_controller.go`:

```go
// isMigratingFromHeadless returns true when spec.mode is online but the existing
// StatefulSet was last configured in headless mode (HEADLESS_AGENT=true is present).
func (r *ReconcileMongoDbReplicaSet) isMigratingFromHeadless(ctx context.Context, rs *mdbv1.MongoDB) (bool, error) {
    if rs.Spec.ConnectionSpec.IsHeadless() {
        return false, nil
    }
    sts := &appsv1.StatefulSet{}
    if err := r.client.Get(ctx, rs.ObjectKey(), sts); err != nil {
        if k8serrors.IsNotFound(err) {
            return false, nil // fresh resource, no migration needed
        }
        return false, err
    }
    for _, c := range sts.Spec.Template.Spec.Containers {
        if c.Name != util.AgentContainerName {
            continue
        }
        for _, e := range c.Env {
            if e.Name == "HEADLESS_AGENT" && e.Value == "true" {
                return true, nil
            }
        }
    }
    return false, nil
}
```

- [ ] **Step 4: Wire migration check into `Reconcile` and implement `migrateHeadlessToOnline`**

Update the `Reconcile` function dispatch block:

```go
// Headless mode bypasses the OM-connected reconcile path entirely.
if rs.Spec.ConnectionSpec.IsHeadless() {
    return r.reconcileHeadless(ctx, rs, log)
}

// Detect and handle headless → online migration before the normal online path.
migrating, err := r.isMigratingFromHeadless(ctx, rs)
if err != nil {
    return r.updateStatus(ctx, rs, workflow.Failed(err), log)
}
if migrating {
    return r.migrateHeadlessToOnline(ctx, rs, log)
}

helper, err := r.newReconcilerHelper(ctx, rs, log)
// ... rest unchanged
```

Add `migrateHeadlessToOnline`:

```go
// migrateHeadlessToOnline transitions a previously headless MongoDB resource to online
// mode by pushing a freshly calculated AC to OM and rolling the StatefulSet.
// All steps are idempotent — safe to re-run at any point.
func (r *ReconcileMongoDbReplicaSet) migrateHeadlessToOnline(ctx context.Context, rs *mdbv1.MongoDB, log *zap.SugaredLogger) (reconcile.Result, error) {
    log.Infow("migrating MongoDB from headless to online mode", "resource", rs.Name)

    // Step 1: Establish OM connection (reuses the same helper as normal online reconcile).
    helper, err := r.newReconcilerHelper(ctx, rs, log)
    if err != nil {
        return r.updateStatus(ctx, rs, workflow.Failed(err), log)
    }

    // Step 2–4: Calculate AC using the existing MongoDB AC builder and push to OM.
    // conn.ReadUpdateDeployment calls ReconcileReplicaSetAC under the hood, which uses
    // the same AC builder as the normal online path.
    if err = helper.pushAutomationConfigToOM(ctx, rs, log); err != nil {
        return r.updateStatus(ctx, rs, workflow.Failed(xerrors.Errorf("migration: failed to push AC to OM: %w", err)), log)
    }

    // Step 5–6: Update StatefulSet to online mode (remove HEADLESS_AGENT, switch agent command).
    // Delegate to the normal online StatefulSet reconcile — it produces the correct online sts.
    if err = helper.reconcileStatefulSet(ctx, rs, log); err != nil {
        return r.updateStatus(ctx, rs, workflow.Failed(xerrors.Errorf("migration: failed to update StatefulSet: %w", err)), log)
    }

    // Step 7: Wait for pods to roll and reconnect to OM.
    ready, err := statefulset.IsReady(ctx, r.client, rs.ObjectKey(), rs.Spec.Members)
    if err != nil {
        return r.updateStatus(ctx, rs, workflow.Failed(err), log)
    }
    if !ready {
        return r.updateStatus(ctx, rs, workflow.Pending("migration in progress: waiting for pods to reconnect to Ops Manager"), log)
    }

    log.Infow("MongoDB migration from headless to online complete", "resource", rs.Name)
    // Fall through to normal online reconcile to reconcile status and watchers.
    return helper.Reconcile(ctx)
}
```

Note: `helper.pushAutomationConfigToOM` and `helper.reconcileStatefulSet` are internal methods on the existing reconciler helper type. Look at how the normal online path calls these steps inside `helper.Reconcile(ctx)` and extract the same calls here. The exact method names depend on what the helper exposes — check the helper type and adapt accordingly.

- [ ] **Step 5: Add migration idempotency test**

```go
func TestMigration_IsIdempotent(t *testing.T) {
    ctx := context.Background()
    rs := DefaultReplicaSetBuilder().
        SetMode(mdbv1.ConnectionModeHeadless).
        Build()
    rs.Spec.Credentials = ""
    rs.Spec.OpsManagerConfig = &mdbv1.PrivateCloudConfig{}
    rs.Spec.CloudManagerConfig = &mdbv1.PrivateCloudConfig{}

    reconciler, client, omConnectionFactory := defaultReplicaSetReconciler(ctx, nil, "", "", rs)
    checkHeadlessReconcileSuccessful(ctx, t, reconciler, rs, client)

    // Patch to online.
    rs.Spec.Mode = mdbv1.ConnectionModeOpsManager
    rs.Spec.Credentials = "my-creds"
    rs.Spec.OpsManagerConfig = &mdbv1.PrivateCloudConfig{ConfigMapRef: mdbv1.ConfigMapRef{Name: "my-om-cm"}}

    // First reconcile: migration in progress (pods not ready yet).
    err := client.Update(ctx, rs)
    require.NoError(t, err)
    res, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: rs.NamespacedName()})
    require.NoError(t, err)

    // Second reconcile: must not error or produce duplicate OM updates.
    res, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: rs.NamespacedName()})
    require.NoError(t, err)
    _ = res

    // OM should have received exactly one AC update regardless of number of reconciles.
    conn := omConnectionFactory.GetConnection()
    conn.(*om.MockedOmConnection).CheckNumberOfUpdateRequests(t, 1)
}
```

- [ ] **Step 6: Run all migration tests**

```bash
go test ./controllers/operator/... -run "TestMigration" -v 2>&1 | tail -30
```

Expected: all PASS.

- [ ] **Step 7: Commit**

```bash
git add controllers/operator/mongodbreplicaset_controller.go \
        controllers/operator/mongodbreplicaset_controller_test.go
git commit -m "feat(headless): implement headless→online migration with idempotent reconcile"
```

---

## Task 6: Backward compatibility and AC determinism tests

- [ ] **Step 1: Write backward-compat test**

Add to `controllers/operator/mongodbreplicaset_controller_test.go`:

```go
// TestBackwardCompat_ExistingCRWithoutModeField verifies that CRs created before
// the mode field existed (mode omitted → defaults to OpsManager) continue to reconcile
// identically to the pre-feature behavior.
func TestBackwardCompat_ExistingCRWithoutModeField(t *testing.T) {
    ctx := context.Background()
    // DefaultReplicaSetBuilder does not set mode — relies on kubebuilder default (OpsManager).
    rs := DefaultReplicaSetBuilder().Build()
    assert.Equal(t, mdbv1.ConnectionMode(""), rs.Spec.Mode) // not explicitly set

    reconciler, client, omConnectionFactory := defaultReplicaSetReconciler(ctx, nil, "", "", rs)
    checkReconcileSuccessful(ctx, t, reconciler, rs, client)

    conn := omConnectionFactory.GetConnection()
    conn.(*om.MockedOmConnection).CheckNumberOfUpdateRequests(t, 1)
}
```

- [ ] **Step 2: Write AC determinism test**

```go
// TestHeadlessACDeterminism verifies that two reconcile runs on the same spec produce
// byte-identical AC Secret content, preventing spurious pod restarts.
func TestHeadlessACDeterminism(t *testing.T) {
    ctx := context.Background()
    rs := DefaultReplicaSetBuilder().
        SetMode(mdbv1.ConnectionModeHeadless).
        Build()
    rs.Spec.Credentials = ""
    rs.Spec.OpsManagerConfig = &mdbv1.PrivateCloudConfig{}
    rs.Spec.CloudManagerConfig = &mdbv1.PrivateCloudConfig{}

    reconciler, client, _ := defaultReplicaSetReconciler(ctx, nil, "", "", rs)
    checkHeadlessReconcileSuccessful(ctx, t, reconciler, rs, client)

    // Read AC Secret after first reconcile.
    secret1 := &corev1.Secret{}
    require.NoError(t, client.Get(ctx, types.NamespacedName{Name: rs.Name + "-config", Namespace: rs.Namespace}, secret1))
    content1 := secret1.Data

    // Second reconcile — spec unchanged.
    checkHeadlessReconcileSuccessful(ctx, t, reconciler, rs, client)

    secret2 := &corev1.Secret{}
    require.NoError(t, client.Get(ctx, types.NamespacedName{Name: rs.Name + "-config", Namespace: rs.Namespace}, secret2))

    assert.Equal(t, content1, secret2.Data, "AC Secret content must be identical across reconcile runs on the same spec")
}
```

- [ ] **Step 3: Run both tests**

```bash
go test ./controllers/operator/... -run "TestBackwardCompat|TestHeadlessACDeterminism" -v 2>&1 | tail -20
```

Expected: both PASS.

- [ ] **Step 4: Commit**

```bash
git add controllers/operator/mongodbreplicaset_controller_test.go
git commit -m "test(headless): add backward-compat and AC determinism tests"
```

---

## Task 7: CRD regeneration

- [ ] **Step 1: Regenerate CRDs and deepcopy**

```bash
make generate manifests 2>&1 | tail -20
```

If `make generate` is not the right target, check `Makefile` for the correct target (search for `controller-gen`).

- [ ] **Step 2: Verify `credentials` is no longer in `required`**

```bash
grep -A5 "required:" config/crd/bases/mongodb.com_mongodb.yaml | grep credentials
```

Expected: no output (credentials removed from required).

- [ ] **Step 3: Verify `mode` enum appears in the CRD**

```bash
grep -A5 "mode:" config/crd/bases/mongodb.com_mongodb.yaml | head -10
```

Expected: output includes `enum: [OpsManager, CloudManager, Headless]`.

- [ ] **Step 4: Run full test suite**

```bash
go test ./... 2>&1 | grep -E "FAIL|ok" | tail -30
```

Expected: all packages pass.

- [ ] **Step 5: Commit**

```bash
git add config/crd/bases/ helm_chart/crds/ public/
git commit -m "chore(headless): regenerate CRDs — remove credentials from required, add mode enum"
```

---

## Task 8: Full lint check

- [ ] **Step 1: Run linter**

```bash
golangci-lint run ./... 2>&1 | head -50
```

Fix any lint errors before proceeding.

- [ ] **Step 2: Commit any lint fixes**

```bash
git add -u
git commit -m "fix(headless): resolve lint warnings"
```

---

## Checklist summary

| Task | Description | Key files |
|------|-------------|-----------|
| 1 | Add `ConnectionMode` type, `mode` field, `IsHeadless()`, update `GetConnectionSpec()` | `api/v1/mdb/mongodb_types.go` |
| 2 | Update webhook validation for headless | `api/v1/mdb/mongodb_validation.go` |
| 3 | Headless agent command builder | `controllers/operator/construct/headless_agent_command.go` |
| 4 | `reconcileHeadless` + `buildHeadlessAutomationConfig` | `controllers/operator/mongodbreplicaset_controller.go` |
| 5 | Headless→online migration | `controllers/operator/mongodbreplicaset_controller.go` |
| 6 | Backward-compat + AC determinism tests | `controllers/operator/mongodbreplicaset_controller_test.go` |
| 7 | CRD regeneration | `config/crd/bases/`, `helm_chart/crds/`, `public/` |
| 8 | Lint | all |
