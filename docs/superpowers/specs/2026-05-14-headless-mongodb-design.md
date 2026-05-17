# Headless MongoDB — Design Spec

**Date:** 2026-05-14  
**Author:** Maciej Karaś  
**Branch:** `decuple-plan-10b-appdb-construct-simplify` (groundwork), new feature branch TBD  
**Related:** [Spike: AppDB Agent Mode Switch – Headless to Online via Meta OM](https://docs.google.com/document/d/1nd2WJSiLF49IJ9IOhyw29r-4Q_s-q5ypVB0S_tQ8TfI)

---

## Goal

Enable running MongoDB via the MCK operator without an Ops Manager connection. Agents run in headless/detached mode — identical to how AppDB and MCO community replicasets work today — but using the full `MongoDB` CRD with its existing feature set (TLS, security, connectivity, multi-cluster).

Additionally, allow a headless MongoDB resource to be migrated in-place to online mode (OpsManager or CloudManager), enabling backup and full OM management without data migration.

**Out of scope:**
- Sharded cluster support for headless mode
- AppDB CRD replacement (groundwork is laid; migration is a follow-on)
- Reversing online→headless migration
- Backup activation via CRD (backup is enabled via OM API post-migration)

---

## Motivation

1. **Reduced OM dependency** — run MongoDB without deploying Ops Manager at all
2. **Community→MongoDB migration path** — headless MongoDB offers the same capability as a community replicaset; users can migrate without switching to OM
3. **AppDB simplification groundwork** — AppDB can eventually become a plain headless MongoDB resource with additional validations, eliminating `AppDBSpec`
4. **Unified controller trajectory** — headless mode in the MongoDB controller is the first step toward merging the MongoDB and AppDB controllers

---

## API Changes

### `ConnectionMode` type and `mode` field on `ConnectionSpec`

File: `api/v1/mdb/mongodb_types.go`

```go
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
    // Required when mode is OpsManager or CloudManager.
    // +optional
    Credentials string         `json:"credentials,omitempty"`
    // +optional
    // +kubebuilder:default=OpsManager
    Mode        ConnectionMode `json:"mode,omitempty"`
}
```

`Credentials` changes from required to optional. The CRD schema `required: [credentials]` entry is removed. Validation moves entirely to the webhook.

### Webhook validation changes

File: `api/v1/mdb/mongodb_validation.go`

- `specWithExactlyOneSchema` only fires when `mode != Headless`
- New validator: when `mode == Headless`, `credentials` and `opsManager`/`cloudManager` must be absent — setting them is a validation error
- Existing validator: when `mode == OpsManager` or `mode == CloudManager`, `credentials` is required and the corresponding config ref must be set

### Example CRs

```yaml
# Headless single-cluster replicaset
apiVersion: mongodb.com/v1
kind: MongoDB
metadata:
  name: my-rs
spec:
  mode: Headless
  members: 3
  type: ReplicaSet
  version: "7.0.0"

# Online (backward-compatible — mode defaults to OpsManager)
apiVersion: mongodb.com/v1
kind: MongoDB
metadata:
  name: my-rs
spec:
  credentials: my-creds
  opsManager:
    configMapRef:
      name: my-om-cm
  members: 3
  type: ReplicaSet
  version: "7.0.0"

# Headless multi-cluster replicaset
apiVersion: mongodb.com/v1
kind: MongoDB
metadata:
  name: my-rs
spec:
  mode: Headless
  topology: MultiCluster
  clusterSpecList:
    - clusterName: cluster1
      members: 2
    - clusterName: cluster2
      members: 1
  type: ReplicaSet
  version: "7.0.0"
```

### Backward compatibility

Existing CRs omit `mode`. The default value `OpsManager` plus existing `credentials`/`opsManager` or `cloudManager` fields means all existing CRs work without any changes.

---

## Controller Design

### Architecture principle

The MongoDB controller is the primary path. `reconcileOnline` is the existing reconcile body — untouched. `reconcileHeadless` is a new path that reuses as much existing MongoDB controller infrastructure as possible. The goal is a single unified controller that handles both modes, with AppDB eventually routing through the same path.

### Reconcile dispatch

```go
func (r *ReplicaSetReconciler) Reconcile(ctx context.Context, mdb *mdbv1.MongoDB) (reconcile.Result, error) {
    // ... existing setup: InitDefaults, status, watchers ...

    if mdb.Spec.Mode == mdbv1.ConnectionModeHeadless {
        return r.reconcileHeadless(ctx, mdb)
    }
    return r.reconcileOnline(ctx, mdb) // existing path, no changes
}
```

### `reconcileHeadless`

**Shared with online path (no duplication):**
- StatefulSet construction (same builder)
- TLS / security setup
- Connectivity / service construction
- Status updates
- Multi-cluster StatefulSet distribution via `ClusterSpecList`

**Headless-specific divergences:**

| Concern | Online | Headless |
|---|---|---|
| OM API calls | Project lookup, AC push | Skipped entirely |
| Automation config destination | Pushed to OM | Written to Secret `<name>-config` |
| Agent command | `-mmsBaseUrl` | `-cluster=<config-file-path>` |
| Agent env vars | `MMS_*` | `HEADLESS_AGENT=true`, `AUTOMATION_CONFIG_MAP=<secret-name>` |
| Readiness check | Pod annotation | `StatefulSet.IsReady()` |
| `agent-downloads` volume | Present | Present (required for future online migration) |
| AC calculation | Existing builder | Same builder — `systemLog` injected via `AddProcessModification` (see below) |

**`systemLog` injection (headless only):**

The automation agent starts mongod with `--fork`, which requires `--logpath` or `--syslog`. The online path derives `systemLog` from the OM-supplied automation config; in headless mode no OM config exists, so the controller must inject it explicitly using `AddProcessModification`:

```go
AddProcessModification(func(_ int, p *automationconfig.Process) {
    automationconfig.ConfigureAgentConfiguration(&automationconfig.SystemLog{
        Destination: automationconfig.File,
        Path:        util.PvcMountPathLogs + "/mongodb.log",
    }, nil, nil, p)
})
```

This must be applied in both the single-cluster (`mongodbreplicaset_controller.go`) and multi-cluster (`mongodbmultireplicaset_controller.go`) headless AC builders.

**Hostname-override ConfigMap (multi-cluster only):**

In multi-cluster mode, pods mount a ConfigMap (`<name>-hostname-override`) that maps each pod name to its external FQDN so the agent registers itself under the correct hostname. This ConfigMap is created by `reconcileHostnameOverrideConfigMap` and must be called explicitly at the start of `reconcileHeadless` before `reconcileHeadlessStatefulSets`. The online path calls it unconditionally; the headless path must do the same.

**Sharded cluster guard:**

```go
if mdb.Spec.ResourceType == mdbv1.ShardedCluster {
    return r.updateStatus(ctx, mdb, status.Error("headless mode does not support sharded clusters"))
}
```

**Multi-cluster:**

Single automation config Secret written in the central cluster. The existing multi-cluster machinery distributes it to member clusters — same pattern as today's multi-cluster MongoDB.

### Migration: headless → online

Triggered by user patching `mode` from `Headless` to `OpsManager` or `CloudManager` and adding `credentials` + `opsManager`/`cloudManager`.

The controller detects the transition by comparing `spec.mode` against the current StatefulSet state (presence of `-cluster` vs `-mmsBaseUrl` in the agent command).

**Migration steps (all idempotent):**

1. Detect mode transition: `spec.mode == OpsManager` or `spec.mode == CloudManager` but StatefulSet still has `-cluster` agent command
2. Look up or create the OM project via `ConnectionSpec`
3. Calculate the automation config using the **existing MongoDB AC builder** (same code path as online reconcile), with adjustments for online mode (proper `mongoDbVersions`, correct TLS derivation, `backupVersions` if backup is enabled)
4. Push the freshly calculated config to OM via `UpdateAutomationConfig`
5. Create an agent API key in OM; store in Secret `<name>-agent-key`
6. Update StatefulSet: remove `HEADLESS_AGENT` / `AUTOMATION_CONFIG_MAP` env vars; replace `-cluster` with `-mmsBaseUrl` + agent key params
7. Wait for `StatefulSet.IsReady()` — pods roll and reconnect to OM

Each step checks actual state before acting — re-running reconcile at any point is safe.

**`HEADLESS_AGENT` env var removal and `set -u` safety:**

`agent-launcher.sh` runs with `set -Eou pipefail` (nounset active). Step 6 removes `HEADLESS_AGENT` from the StatefulSet spec, causing pods to roll without the env var present. Any reference to `${HEADLESS_AGENT}` in the script will abort the process with "unbound variable" if the var is absent. All references must use the default-expansion form `${HEADLESS_AGENT:-}` so the script handles both the headless case (var set to `"true"`) and the post-migration case (var absent).

---

## RBAC Requirements

### `mongodb-kubernetes-database-pods` Role

The readiness probe in each database pod runs as the `mongodb-kubernetes-database-pods` ServiceAccount. It needs `get` on `secrets` (to read the automation config Secret) and `get`/`patch` on `pods`. This SA has always existed, but its Role and RoleBinding were absent from the helm chart and from the `kubectl-mongodb` multi-cluster setup tooling.

Both must be present in every namespace where database pods run:

```yaml
kind: Role
apiVersion: rbac.authorization.k8s.io/v1
metadata:
  name: mongodb-kubernetes-database-pods
rules:
  - apiGroups: [""]
    resources: ["secrets"]
    verbs: ["get"]
  - apiGroups: [""]
    resources: ["pods"]
    verbs: ["get", "patch"]
---
kind: RoleBinding
apiVersion: rbac.authorization.k8s.io/v1
metadata:
  name: mongodb-kubernetes-database-pods
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: mongodb-kubernetes-database-pods
subjects:
  - kind: ServiceAccount
    name: mongodb-kubernetes-database-pods
```

This applies to both single-cluster and every member cluster in multi-cluster deployments. The helm chart (`helm_chart/templates/database-roles.yaml`) and the `kubectl-mongodb` `createDatabaseRoles`/`copyDatabaseRoles` functions must both create it.

---

## AppDB Groundwork

This feature explicitly does **not** change the AppDB CRD or controller. However, the headless path in the MongoDB controller is designed so that AppDB can eventually route through it:

- The headless AC generation, Secret writing, agent command construction, and readiness check are implemented as standalone functions (not inlined in the reconciler)
- AppDB-specific constraints (no sharding, member count limits, required security settings) are implemented as validators — the same validators that would apply when AppDB becomes a headless MongoDB resource
- No AppDB logic is duplicated into the MongoDB controller; AppDB construction code (Plan 10b packages) is not the implementation source — it is the reference

---

## Testing

### Unit tests

| Test | Coverage |
|---|---|
| `reconcileHeadless` ReplicaSet, single-cluster | AC written to Secret; `HEADLESS_AGENT=true`; `-cluster` in agent command; no OM API calls |
| `reconcileHeadless` ShardedCluster | Returns validation error immediately |
| `reconcileHeadless` multi-cluster | StatefulSet distributed across member clusters; single AC Secret in central cluster |
| Mode transition detection | Controller identifies headless→OpsManager from StatefulSet state |
| Migration happy path | AC calculated via existing builder, pushed to OM, StatefulSet updated, `HEADLESS_AGENT` removed |
| Migration idempotency | Re-running reconcile mid-migration produces no additional changes |
| Backward compatibility | CR without `mode` field reconciles identically to existing online path |
| Webhook: `mode=Headless` | `credentials`/`opsManager` not required |
| Webhook: `mode=OpsManager` | `credentials` + `opsManager` required; validation error if absent |
| AC determinism | Two reconcile runs on the same spec produce byte-identical AC Secret content |

### e2e tests

- Single-cluster headless ReplicaSet reaches Running with no OM CR in namespace
- Multi-cluster headless ReplicaSet reaches Running
- Headless → OpsManager migration: pods roll, project appears in OM, OM shows deployment as managed
- Community → headless MongoDB migration: existing community replicaset data accessible from headless MongoDB resource

---

## Key Files

| File | Change |
|---|---|
| `api/v1/mdb/mongodb_types.go` | Add `ConnectionMode` type + constants; change `Credentials` to optional; add `Mode` field to `ConnectionSpec` |
| `api/v1/mdb/mongodb_validation.go` | Make `specWithExactlyOneSchema` conditional on `mode != Headless`; add headless-specific validator |
| `config/crd/bases/mongodb.com_mongodb.yaml` | Regenerated — remove `credentials` from `required`; add `mode` enum |
| `controllers/operator/mongodb_reconciler.go` (or equivalent) | Add mode dispatch; implement `reconcileHeadless`; implement migration detection + steps |
| `controllers/operator/construct/` | New/updated agent command builder for headless; `agent-downloads` emptyDir always present |
| `controllers/operator/appdbreplicaset_controller.go` | No changes (AppDB migration is follow-on) |
