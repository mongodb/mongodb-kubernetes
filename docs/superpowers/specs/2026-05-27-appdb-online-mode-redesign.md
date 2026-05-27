# AppDB Online Mode Redesign

**Branch:** `maciejk/om-backup-poc`
**Spike doc:** `2026-03-03-appdb-agent-mode-switch-spike.md`

## Goal

Improve the PoC implementation of AppDB headless→online mode switch. Three concerns addressed:

1. **API shape**: replace the bespoke `MetaOMRef` struct with the standard MCK `ConnectionSpec` pattern so the UX is identical to any MongoDB CR connecting to an OM instance.
2. **Reconcile order and monitoring**: establish the external OM connection before monitoring is configured; use `monitoringVersions` in the AC (regular MongoDB pattern) instead of a monitoring sidecar.
3. **AC flow**: build the Automation Config from the CR (not the headless Secret) and push it through the standard deploy path, not as a side-effect inside `reconcileOMConnection`.

No `MetaOM` naming anywhere in Go code. It is fine in pytest fixtures.

---

## Background

The spike branch (`maciejk/om-backup-poc`) already implements the mode switch end-to-end but with several structural issues identified post-PoC:

- `ManagedByMetaOM *MetaOMRef` — custom struct with a CR-to-CR reference (`Name`, `Namespace` of the Meta OM CR). Inconsistent with how every other MongoDB resource declares its OM connection.
- `reconcileManagedByMetaOM` runs *after* `tryConfigureMonitoringInOpsManager`, so monitoring setup has no project context yet.
- AC sanitisation and push live inside `reconcileManagedByMetaOM`, bypassing the standard deploy flow.
- Monitoring in online mode uses a sidecar container + dedicated Secret, which is the headless-specific approach. Regular MongoDB uses `monitoringVersions` in the AC instead.

---

## Approach: Two PRs

### PR 1 — API + Connection Pipeline

#### `project.Reader` interface

Replace `metav1.Object` embedding with a single `GetName() string` method — the only method from `metav1.Object` actually called in `ReadConfigAndCredentials` (line 35, `project.go`). All existing implementors already have `GetName()` via embedded `ObjectMeta`; no other files change.

```go
type Reader interface {
    GetName() string
    GetProjectConfigMapName() string
    GetProjectConfigMapNamespace() string
    GetCredentialsSecretName() string
    GetCredentialsSecretNamespace() string
}
```

#### `AppDBSpec` API change (`api/v1/om/appdb_types.go`)

Remove `ManagedByMetaOM *MetaOMRef` and the `MetaOMRef` struct entirely.

Add:

```go
// Connection, when set, switches AppDB agents from headless mode to online
// mode connected to the referenced external Ops Manager instance.
// Follows the same ConfigMap + credentials Secret pattern as any MongoDB CR.
// +optional
Connection *ConnectionSpec `json:"connection,omitempty"`
```

`AppDBSpec` implements `project.Reader` (five methods, only valid when `Connection != nil`):

```go
func (s *AppDBSpec) GetName() string                       { return s.Name() }
func (s *AppDBSpec) GetProjectConfigMapName() string       { return s.Connection.GetProjectConfigMapName() }
func (s *AppDBSpec) GetProjectConfigMapNamespace() string  { return s.Namespace }
func (s *AppDBSpec) GetCredentialsSecretName() string      { return s.Connection.Credentials }
func (s *AppDBSpec) GetCredentialsSecretNamespace() string { return s.Namespace }
```

#### Status (`api/v1/om/opsmanager_types.go`)

Rename `MetaOMGroupId` → `ExternalGroupID` in `AppDbStatus`:

```go
// ExternalGroupID stores the project (group) ID assigned by the external OM
// once the AppDB has been registered. Used for observability.
// +optional
ExternalGroupID string `json:"externalGroupID,omitempty"`
```

#### `reconcileOMConnection` (renamed from `reconcileManagedByMetaOM`)

Replaces custom credential-reading + project-creation logic with the standard pipeline. AC push logic moves to PR 2.

```go
func (r *ReconcileAppDbReplicaSet) reconcileOMConnection(
    ctx context.Context,
    opsManager *omv1.MongoDBOpsManager,
    log *zap.SugaredLogger,
) (om.Connection, construct.AgentConnectionConfig, workflow.Status) {

    projectConfig, credentials, err := project.ReadConfigAndCredentials(
        ctx, r.client, r.client, &opsManager.Spec.AppDB, log)
    // handle err

    conn, _, err := connection.PrepareOpsManagerConnection(
        ctx, r.client, projectConfig, credentials,
        r.omConnectionFactory, opsManager.Namespace, log)
    // handle err

    opsManager.Status.AppDbStatus.ExternalGroupID = conn.GroupID()

    return conn, construct.AgentConnectionConfig{
        Enabled: true,
        Server:  projectConfig.BaseURL,
        GroupID: conn.GroupID(),
    }, workflow.OK()
}
```

`PrepareOpsManagerConnection` already handles `GetOrCreateProject` and agent API key Secret creation — no manual steps needed.

**Agent key Secret naming change**: the PoC used `{appdb-name}-meta-om-agent-key` (custom). `PrepareOpsManagerConnection` uses `agents.ApiKeySecretName(groupId)` → `{groupId}-group-secret` (standard pattern used by all MongoDB CRs). Update any references to the old secret name in e2e tests.

#### Naming changes in `appdb_construction.go`

- `MetaOMEnvVars` → `AgentConnectionConfig`
- `AppDBStatefulSetOptions.MetaOM` → `AppDBStatefulSetOptions.Connection AgentConnectionConfig`
- All `MetaOM*`-prefixed symbols renamed accordingly
- All `ManagedByMetaOM != nil` guards → `Connection != nil`

---

### PR 2 — Reconcile Order + AC Flow + Monitoring

#### Reconcile order (`ReconcileAppDB`)

`reconcileOMConnection` runs before `tryConfigureMonitoringInOpsManager`. `tryConfigureMonitoringInOpsManager` is only called when `AppDB.Connection == nil` (headless mode — no change to its signature or body):

```go
var externalConn om.Connection
if opsManager.Spec.AppDB.Connection != nil {
    externalConn, agentConnConfig, ws = r.reconcileOMConnection(ctx, opsManager, log)
    if !ws.IsOK() { ... }
}

if opsManager.Spec.AppDB.Connection == nil {
    podVars, err = r.tryConfigureMonitoringInOpsManager(ctx, opsManager, ...)
}
```

#### Monitoring in online mode

`tryConfigureMonitoringInOpsManager` is a headless-only concept and is not called in online mode. Monitoring in online mode follows the regular MongoDB pattern: `configureMonitoring(ac)` (already called inside `buildAppDbAutomationConfig`) adds `monitoringVersions` entries for each AppDB process into the AC. Since the AC is pushed to the external OM (see below), the OM's monitoring infrastructure picks it up — no sidecar container needed.

When `Connection != nil`:
- `ShouldEnableMonitoring` must be gated explicitly on `Connection == nil` (not on `podVars.ProjectID != ""`, because `podVars.ProjectID` may still be set from `AgentConnectionConfig.GroupID` for other purposes). Update `ShouldEnableMonitoring` or its call site accordingly.
- Skip monitoring AC Secret creation

#### AC flow

`deployAutomationConfigAndWaitForAgentsReachGoalState` accepts the external connection directly (not via `AppDBStatefulSetOptions`, which is for StatefulSet construction only):

```go
func (r *ReconcileAppDbReplicaSet) deployAutomationConfigAndWaitForAgentsReachGoalState(
    ctx context.Context,
    log *zap.SugaredLogger,
    opsManager *omv1.MongoDBOpsManager,
    externalConn om.Connection,      // non-nil → push to external OM
    allStatefulSetsExist bool,
    appdbOpts construct.AppDBStatefulSetOptions,
) workflow.Status
```

Inside `deployAutomationConfig` (the lowest-level writer):
- `externalConn == nil` → write AC to Secret (headless, unchanged)
- `externalConn != nil` → call `externalConn.UpdateAutomationConfig(ac)`, skip Secret write

The AC is built fresh from the CR (`buildAppDbAutomationConfig`) in both cases — no longer read from the existing headless Secret.

`stripUnsupportedACFields` stays with a comment:
```go
// TODO: verify with e2e and remove if fresh-built AC needs no sanitisation
```

#### AC Secret and volume mount

Already gated by `!opts.Connection.Enabled` from PR 1. No additional change needed.

#### Multi-cluster: agent key Secret replication

Multi-cluster headless AppDB is already fully working. For online mode, most of the multi-cluster flow is unchanged or simpler (AC push becomes one `UpdateAutomationConfig` call instead of per-cluster Secret writes; `AgentConnectionConfig` env vars are identical across clusters).

The one gap: `PrepareOpsManagerConnection` creates `{groupId}-group-secret` once in the operator (central cluster) namespace. AppDB pods in other member clusters need to mount this Secret to read their agent API key. Add `replicateAgentKeySecret` in `ReconcileAppDB` after `reconcileOMConnection`, following the same pattern as the existing `replicateTLSCAConfigMap`:

```go
// replicateAgentKeySecret copies the agent key Secret created by PrepareOpsManagerConnection
// to all healthy member clusters so each AppDB pod can mount it.
func (r *ReconcileAppDbReplicaSet) replicateAgentKeySecret(
    ctx context.Context,
    opsManager *omv1.MongoDBOpsManager,
    groupID string,
    log *zap.SugaredLogger,
) error {
    secretName := agents.ApiKeySecretName(groupID)
    agentKeySecret, err := r.SecretClient.ReadSecret(ctx, kube.ObjectKey(operatorNamespace(), secretName), "")
    // handle err
    for _, mc := range r.GetHealthyMemberClusters() {
        if err := mc.SecretClient.CreateOrUpdateSecret(ctx, opsManager.Namespace, secretName, agentKeySecret); err != nil {
            return err
        }
    }
    return nil
}
```

Call site in `ReconcileAppDB` (only when `Connection != nil`):

```go
externalConn, agentConnConfig, ws = r.reconcileOMConnection(ctx, opsManager, log)
// ...
if err := r.replicateAgentKeySecret(ctx, opsManager, agentConnConfig.GroupID, log); err != nil { ... }
```

---

## What `reconcileOMConnection` does after both PRs

Three lines of substance:

1. `ReadConfigAndCredentials` — reads project ConfigMap + credentials Secret
2. `PrepareOpsManagerConnection` — creates/gets project in external OM, provisions agent API key Secret
3. Build and return `AgentConnectionConfig{Enabled, Server, GroupID}`

The function is thin by design: all heavy lifting is delegated to the standard pipeline.

---

## Out of Scope

- `stripUnsupportedACFields` removal (pending e2e verification)
- Reverting from online → headless mode
- Automated PITR restore via MCK
