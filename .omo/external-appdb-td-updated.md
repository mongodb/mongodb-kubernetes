# MCK: Ops Manager Backup — External AppDB

Author: Maciej Karas, Igor Karpukhin
Date: 2026-07-13 (updated 2026-08-06)
Jira Epic: CLOUDP-398575

## Phasing

This feature is delivered in phases:

- **Phase 1 (current)**: Single-cluster support only. The external AppDB must be a `MongoDB` CR (not `MongoDBMultiCluster`). TLS/CA parity for external AppDB is part of Phase 1 and is implemented in a follow-up PR (#1468) that ships immediately after the base stack.
- **Phase 2 (future)**: Multi-cluster support via `MongoDBMultiCluster` CR. This includes multi-cluster topology, cross-cluster ownership handoff, and per-cluster finalizer state.

## Product Goals and Summary

Enable customers to migrate Ops Manager's existing, internal AppDB to an external AppDB — a regular MongoDB Custom Resource that the Kubernetes Operator already knows how to back up, monitor, and manage. Once migrated, the full backup/restore feature set becomes available for AppDB with no new backup mechanism to build. This migration works by re-pointing ownership of AppDB's existing StatefulSet and its Persistent Volumes from the MongoDBOpsManager resource to a MongoDB Custom Resource, avoiding data movement or recreation.

## Goals

- Enable Ops Manager's AppDB to be backed up and restored the same way customers already back up their own MongoDB deployments.
- Customers should be able to adopt this incrementally on an existing Ops Manager deployment without data movement or StatefulSet/PVC recreation during ownership transfer.
- Support starting with external AppDB for new Ops Manager deployments (requires an existing management-plane Ops Manager — "Meta OM" — to manage the MongoDB CR).
- Support reverting to internal AppDB configuration if needed.
- ~~Support for both single-cluster and multi-cluster AppDB topologies from day one.~~ (Phase 2 — multi-cluster support is deferred, see Phasing above.)

## Existing Implementation Overview

Ops Manager's AppDB — the internal MongoDB replica set backing OM's own metadata, monitoring state, and automation configs — currently has no supported, independent backup/recovery story. If Ops Manager is lost, customers lose the ability to manage their MongoDB clusters and, critically, to restore from backup. The only current workaround, S3 backup re-import, requires a full rebuild of Ops Manager and reimporting every cluster from scratch, which results in loss of all historical data; there is no equivalent path for Blockstore/Filesystem backups.

## Proposed Implementation

The implementation transitions the Ops Manager AppDB from an implicit, internally managed component to an explicit link referencing a MongoDB Custom Resource (CR). This design ensures that the operator manages the lifecycle, credentials, and connection strings end-to-end without manual secret manipulation.

### Key Architectural Components

- **API & Resource Linking**: We introduce `externalApplicationDatabaseRef` field to `MongoDBOpsManagerSpec` and a `spec.role: AppDB` marker on the target MongoDB CR. This enables explicit mapping between an Ops Manager instance and its database. (Phase 2 will add `MongoDBMultiCluster` support.)

- **Deterministic Naming Convention**: To eliminate the need for manual credential copying, all AppDB-role CRs must follow the naming convention `<om-name>-db`. This allows both the Ops Manager and MongoDB controllers to derive the well-known password secret name (`<om-name>-db-om-password`) independently and consistently.

- **Direct Connection String Computation**: The referenced CR does not generate its own connection-string secret. Instead, the Ops Manager controller computes the connection string directly from the live CR object using the shared `BuildConnectionString` method. The result is written to the primary OM's fixed, long-lived connection-string secret. This design preserves the pod's volume mount identity, preventing unnecessary restarts when switching between internal and external management modes.

- **TLS and CA management**: Because we can now use the MongoDB CRD as AppDB, the TLS and CA have to be configured for it. The StatefulSet from OpsManager mounts a CA ConfigMap to trust the TLS cert of AppDB. The `AppDBReconciler` interface now exposes a `GetAppDBConfig` method that returns an `AppDBConfig` struct containing the connection string, TLS-enabled flag, and CA ConfigMap name. For the internal AppDB, these are derived from `opsManager.Spec.AppDB`. For the external AppDB, the OM controller resolves the referenced MongoDB CR's `security.tls.ca` field and TLS flag via the `ExternalAppDB` interface (`GetCAConfigMapName()` and `IsTLSEnabled()`). The `AppDBConfig` is passed through the OM reconciliation flow to `ensureConfiguration` (which sets `mms.mongoSSL` and `mms.mongoCA`), `replicateAppDBTLSCAInMemberClusters` (which replicates the CA ConfigMap to member clusters), and the StatefulSet construction (via `WithAppDBTLSCAConfigMapName`). This is implemented in PR #1468.

- **Two-Annotation Migration Protocol**: The implementation uses two distinct annotations:
  - `mongodb.com/appdb-migration-ready`: set by the OM controller during forward migration (internal → external)
  - `mongodb.com/appdb-reverse-migration-ready`: set by the OM controller during reverse migration (external → internal)

  Both annotations persist through ownership transfer and are cleared when the StatefulSet is rebuilt (pod template reshape), not at the moment of adoption/reclaim. This lifecycle ensures that the controllers can detect "pending reshape" state and force StatefulSet-first deployment ordering.

### Idempotency and Robustness

Every step operates on a "check-current-state-then-converge" principle. Reconciliation logic re-evaluates the live state on every tick, and all write operations (creating/patching secrets, updating OwnerReferences, setting annotations) are overwrite-safe. This ensures that the system can recover from interruptions at any point in the migration flow without requiring manual intervention or persistent "progress" records.

## Algorithm(s)

The implementation relies on three reconciliation procedures:

### Procedure 1 (Fresh Start)

When a MongoDB CR with `spec.role: AppDB` is created, the MongoDB controller handles validation, ensures the `mongodb-ops-manager` user exists, and creates the StatefulSet. The OM controller, upon seeing a reference, validates it and computes the connection string directly, establishing a watch on the external resource.

**Prerequisite**: The MongoDB CR must be managed by an existing Ops Manager (the "Meta OM") — the MongoDB reconciler requires an Ops Manager connection to function (project config, credentials, agent registration). A new Ops Manager cannot start without AppDB, while the AppDB-role MongoDB CR cannot reconcile without an available Ops Manager.

1. **Webhook validation** (admission-time): reject unless all of —
   - `spec.security.authentication.enabled: true` with SCRAM in modes
   - `spec.security.authentication.ignoreUnknownUsers: true`
   - `spec.members >= 3`
   - MongoDB version >= 4.0.0
   - Single-cluster topology only (multi-cluster rejected in Phase 1)

2. **MongoDB controller reconciles.** StatefulSet ownership check finds no StatefulSet at all for this CR's name → no adoption gate applies, proceed directly to normal creation.

3. **Ensure the `mongodb-ops-manager` user**, mirroring the internal AppDB reconciler's `ensureAppDbPassword` + `AppDBSpec.GetAuthUsers()` pattern — relocated into this controller's own logic rather than going through a separate MongoDBUser CR:
   - Check whether the well-known password secret for this CR's derived OM name (`<om-name>-db-om-password`) already exists — it doesn't (fresh start).
   - Generate a new password, create the secret under that name.
   - Inject a synthetic `mongodb-ops-manager` user (roles from shared `AppDBUserRoles` in `appdb_types.go`) directly into this CR's own automation-config user list.

4. **Create the StatefulSet**, with this CR's own OwnerReference set on it. This CR never creates a connection-string secret of its own.

5. Resource reaches Running.

6. **Separately, order-independent**: whenever `spec.externalApplicationDatabaseRef` is set on an OM CR pointing at this resource, the OM controller validates that the reference's name matches the required naming convention and that `role: AppDB` and version >= 4.0.0 hold on the target, skips `ReconcileAppDB()`/`SetupCommonWatchers`, fetches the target CR, and computes Primary OM's fixed connection-string secret directly via `BuildConnectionString`, using the shared password secret's credentials. The OM controller also establishes a watch on this CR. No detach steps run in this procedure.

### Procedure 2 (Forward Migration)

Upon creation of a companion MongoDB CR and reference update, the OM controller detaches the existing internal AppDB by stripping OwnerReferences from the StatefulSet and annotating it as migration-ready. The MongoDB controller then performs an adoption gate check (ready annotation + OwnerReference stripped), adopts the StatefulSet, and reuses the existing credentials. The OM controller subsequently updates the Primary OM connection string secret.

1. **Trigger**: user creates a MongoDB CR named identically to the existing AppDB StatefulSet (`<om-name>-db`) with `spec.role: AppDB`, **and** sets `spec.externalApplicationDatabaseRef` on the OM CR to point at it. These are companion actions performed together.

2. **OM controller, seeing the new reference, performs a one-time detach** (idempotent):
   a. Skip `ReconcileAppDB()` — conditional on `ExternalApplicationDatabaseRef == nil`.
   b. Skip `SetupCommonWatchers` for the AppDB objects — conditional on `ExternalApplicationDatabaseRef == nil`.
   c. Validate the reference: fetch the target, fail fast if `spec.role != "AppDB"` or its name doesn't match `<om-name>-db`.
   d. Strip OwnerReferences from the AppDB StatefulSet (STS and shared secrets only — no ConfigMaps are transferred).
   e. Only after a–d succeed: annotate the StatefulSet with `mongodb.com/appdb-migration-ready: "true"`.

   **Note on staged handover**: Only the StatefulSet OwnerReferences are stripped by the OM controller. The password and keyfile secrets are subsequently claimed by the MongoDB CR controller via `claimAppDBRoleSecrets`. No ConfigMaps are transferred.

   **Note on annotation lifecycle**: The `mongodb.com/appdb-migration-ready` annotation persists through the ownership transfer and is not cleared at adoption. It is cleared when the MongoDB controller redeploys the StatefulSet (pod template reshape), because the fresh StatefulSet object built by `construct.DatabaseStatefulSet` does not carry migration annotations. The annotation's persistence is used by `publishAutomationConfigFirst` to force StatefulSet-first deployment ordering during the reshape.

3. **MongoDB controller reconciles**:
   - StatefulSet ownership check: the StatefulSet exists but does not carry this CR's own OwnerReference → foreign StatefulSet, takeover adoption gate applies.
   - **Gate check**: the `mongodb.com/appdb-migration-ready: "true"` annotation must be present, **and** the StatefulSet must no longer carry the OM's OwnerReference. Either signal being unsatisfied blocks adoption.
   - Once both checks pass: adopt — set this CR's own OwnerReference on the StatefulSet. The annotation is kept (not cleared) for reshape detection.
   - Ensure the `mongodb-ops-manager` user: password secret already exists (same secret internal AppDB was using) → reuse those exact credentials unchanged.
   - This CR never creates a connection-string secret of its own.

4. **OM controller** computes Primary OM's connection string directly from the MongoDB CR via `BuildConnectionString` and writes it into Primary OM's own **fixed** connection-string secret. The OM controller also establishes a watch on the referenced CR.

### Procedure 3 (Reverse Migration)

Reverse migration is **annotation-based**, not finalizer-based. The safe sequence is: remove `externalApplicationDatabaseRef` from the OM CR → OM sets the reverse migration annotation → MongoDB CR strips its OwnerReference → OM reclaims the StatefulSet and secrets → **then** delete the MongoDB CR.

**Why no finalizer**: The implementation uses an annotation-based handshake instead of a finalizer. Deleting the MongoDB CR while it still owns the StatefulSet triggers immediate Kubernetes garbage collection, causing AppDB downtime. The safe sequence requires removing the OM reference first, waiting for the ownership handoff to complete, and only then deleting the MongoDB CR.

**Why resources are not cleaned up after migration**: Removing the project in OM would make it impossible to go back and read historical metrics. It is the user's responsibility to clean up the project after migration. This must be documented in the user-facing documentation.

1. **Trigger**: user removes `spec.externalApplicationDatabaseRef` from the OM CR (and restores `spec.applicationDatabase` if needed). The MongoDB CR remains alive during the handoff.

2. **OM controller** sees `externalApplicationDatabaseRef` removed → selects internal AppDB reconciler → `ensureAppDBStatefulSetOwnership` finds the StatefulSet is foreign-owned (by the MongoDB CR):
   - Sets `mongodb.com/appdb-reverse-migration-ready: "true"` annotation on the StatefulSet.
   - Returns Pending: "waiting for MongoDB controller to release AppDB StatefulSet".

3. **MongoDB controller** sees the `mongodb.com/appdb-reverse-migration-ready: "true"` annotation:
   - Strips its own OwnerReference from the StatefulSet.
   - Returns Pending: "This AppDB resource is under Reverse Migration to Ops Manager CR".

4. **OM controller** reclaims:
   - Sees the StatefulSet is now ownerless with the reverse annotation present.
   - Sets its own OwnerReference on the StatefulSet.
   - Reclaims the shared secrets (password, keyfile).
   - The `mongodb.com/appdb-reverse-migration-ready` annotation persists and is used by `allStatefulSetsExistsInValidState` to force StatefulSet-first deployment ordering during the reshape.
   - Resumes `ReconcileAppDB()` and `SetupCommonWatchers`.
   - Reads the password via existing `ensureAppDbPassword` logic → same shared secret, no rotation.
   - Connection string computed the internal way again.

5. **After handoff completes**: the user may delete the MongoDB CR. The `OnDelete` handler for AppDB-role CRs intentionally skips Ops Manager state cleanup — the project is left stale and the user is responsible for cleaning it up.

6. Internal AppDB management resumes. The StatefulSet pod template is rewritten back to the internal-AppDB shape (container set, env vars) — a rolling restart. The annotation is cleared by the StatefulSet rebuild.

**Fallback reverse migration** (documented variant): If the user deletes the MongoDB CR first (without removing the reference), the StatefulSet and shared secrets are garbage-collected. The AppDB is recreated from scratch with retained PVCs. This path accepts downtime and credential rotation. This variant is tested in the e2e suite.

## Design

### API Changes

Added `ExternalApplicationDatabaseRef` to `MongoDBOpsManagerSpec` and a `Role` field to the MongoDB CR, allowing explicit linking between Ops Manager and its AppDB.

```go
// AppDB configures the internally-managed Application Database. Required unless
// ExternalApplicationDatabaseRef is set, in which case it may be omitted entirely.
// +optional
AppDB *AppDBSpec `json:"applicationDatabase,omitempty"`

// ExternalApplicationDatabaseRef references a MongoDB resource
// to use as this Ops Manager's AppDB, instead of the internally-managed one.
// +optional
ExternalApplicationDatabaseRef *ExternalApplicationDatabaseRef `json:"externalApplicationDatabaseRef,omitempty"`

type ExternalApplicationDatabaseRef struct {
    // Name of the MongoDB resource to use as the external AppDB.
    // Must be in the same namespace as the MongoDBOpsManager resource.
    // Must follow the naming convention: <om-name>-db
    // +kubebuilder:validation:Required
    Name string `json:"name"`

    // Kind of the referenced resource.
    // Phase 1: only MongoDB is supported. MongoDBMultiCluster will be added in Phase 2.
    // +kubebuilder:validation:Enum=MongoDB
    // +kubebuilder:validation:Required
    Kind string `json:"kind"`

    // Namespace is a transient field populated from the OM's namespace.
    // Not serialized in the API.
    Namespace string `json:"-"`
}
```

```go
// Role marks this resource as playing a special role for another MongoDB
// Kubernetes resource. Currently only AppDB is supported, marking this
// resource as the externally-managed Application Database for a
// MongoDBOpsManager resource.
// +kubebuilder:validation:Enum=AppDB
// +optional
Role string `json:"role,omitempty"`
```

The `ExternalApplicationDatabaseRef` points to a MongoDB custom resource that customers create in order to be able to back up their AppDB. The `Role` field is required to define if the resource acts as AppDB. **Phase 1 supports only `Kind: MongoDB`**. `MongoDBMultiCluster` support is planned for Phase 2.

### Naming Conventions

Enforces a strict `<om-name>-db` naming convention for the external CR, allowing deterministic derivation of secret names shared between internal and external modes, eliminating the need for credential copying.

The shared password secret is named `<om-name>-db-om-password` (derived as `OpsManagerUserPasswordSecretName(<appdb-name>)` where `<appdb-name>` = `<om-name>-db`).

### AppDB Configuration and TLS/CA Management

The `AppDBReconciler` interface exposes a `GetAppDBConfig` method that returns an `AppDBConfig` struct:

```go
type AppDBConfig struct {
    IsTLSEnabled     bool
    CAConfigMapName  string
    ConnectionString string
}
```

- **Internal AppDB** (`ReconcileAppDbReplicaSet`): derives `IsTLSEnabled` from `opsManager.Spec.AppDB.GetSecurity().IsTLSEnabled()`, `CAConfigMapName` from `opsManager.Spec.AppDB.GetCAConfigMapName()`, and `ConnectionString` from `buildMongoConnectionUrl`.
- **External AppDB** (`ReconcileExternalAppDBReplicaSet`): resolves the referenced MongoDB CR via the `ExternalAppDB` interface, which now includes `GetCAConfigMapName()` (reads `security.tls.ca`) and `IsTLSEnabled()` (reads the TLS flag). The connection string is computed via `BuildConnectionString`.

The `AppDBConfig` is threaded through the OM reconciliation flow:
- `ensureConfiguration` sets `mms.mongoSSL` and `mms.mongoCA` from `appDBConfig.IsTLSEnabled` and `appDBConfig.CAConfigMapName`.
- `replicateAppDBTLSCAInMemberClusters` replicates the CA ConfigMap to member clusters using `appDBConfig.CAConfigMapName`.
- StatefulSet construction receives the CA ConfigMap name via the `WithAppDBTLSCAConfigMapName` construct option, which mounts it as a volume on both OM and Backup Daemon pods.

The `GetAppDbCA()` method on `MongoDBOpsManagerSpec` has been removed — TLS/CA resolution is now unified through `AppDBConfig` for both internal and external modes.

### Connection String Management

The referenced MongoDB CR **never creates its own connection-string secret.** Instead, the OM controller computes Primary OM's connection string itself, directly from the live CR object:

- The OM controller fetches the referenced MongoDB object and calls its exported `BuildConnectionString(username, password, scheme, connectionParams)` method, using credentials from the shared password secret.
- The OM controller writes the result directly into Primary OM's own **fixed** connection-string secret — the same secret used for internal AppDB, same name regardless of mode.
- To recompute the connection string whenever the external AppDB's live state changes, the OM controller establishes a dynamic watch on the referenced CR.
- The password is read via `secret.ReadKey` through the Vault-aware `SecretClient`, not via the raw Kubernetes client.

### Annotations

Two distinct annotations are used for migration coordination:

| Annotation | Direction | Set by | Cleared by |
|---|---|---|---|
| `mongodb.com/appdb-migration-ready` | Forward (internal → external) | OM controller (`requestAppDBForwardMigration`) | StatefulSet rebuild (pod template reshape) |
| `mongodb.com/appdb-reverse-migration-ready` | Reverse (external → internal) | OM controller (`requestAppDBReverseMigration`) | StatefulSet rebuild (pod template reshape) |

Both annotations **persist through ownership transfer** and are not cleared at the moment of adoption/reclaim. They are cleared when the controller redeploys the StatefulSet with a fresh pod template, because the constructed StatefulSet object does not carry migration annotations. This lifecycle ensures controllers can detect "pending reshape" state and force StatefulSet-first deployment ordering.

### Status Reporting

| Phase | AppDB Status | Meaning |
|---|---|---|
| External mode (stable) | `Disabled` | AppDB is managed externally, OM controller skips internal reconciliation |
| Forward migration (detach) | `Failed` (on OM status) | Reference validation or detach error |
| Reverse migration (waiting) | `Pending` | "waiting for MongoDB controller to release AppDB StatefulSet" |
| Reverse migration (MongoDB side) | `Pending` | "This AppDB resource is under Reverse Migration to Ops Manager CR" |

### Idempotency

All reconciliation steps are 'check-current-state-then-converge' operations. State is re-evaluated on every reconcile, and writes are overwrite-safe (using `secret.CreateOrUpdate` and `secret.EnsureSecretWithKey`), ensuring robustness against partial failures.

## Design Alternative(s)

We tested the alternative solution, where we enable backups for the existing AppDB when having MetaOM, but it turned out the PITR process fails because both mongod and the agent failed to start as they were fighting for the same resource.

## Testing & QA

The 3 procedures are covered with e2e tests:
- **Procedure 1 (Fresh Start)**: `om_external_appdb_fresh.py` — `TestFreshStartExternalAppDB`
- **Procedure 2 (Forward Migration)**: `om_external_appdb_forward.py` — `TestSentinelDocSurvivesForwardMigration`
- **Procedure 3 (Reverse Migration — graceful)**: `om_external_appdb_fresh.py` — `TestReverseMigrationAfterFreshStart` (remove reference first, delete CR after handoff)
- **Procedure 3 (Reverse Migration — fallback)**: `om_external_appdb_forward.py` — `TestReverseMigrationAfterForwardMigration` (delete CR first, accept downtime and recreation)

All e2e tests verify no migration annotations remain on the StatefulSet after migration completes.

## Documentation

Users must be informed that:
- The project created for the AppDB-role MongoDB CR is not automatically cleaned up after reverse migration. Removing the project in OM would make it impossible to go back and read historical metrics. It is the user's responsibility to clean up the project.
- The safe reverse migration sequence is: remove `externalApplicationDatabaseRef` → wait for OM to reclaim → then delete the MongoDB CR. Deleting the CR first causes downtime.

## Known Limitations

- This project does not provide a way to back up or restore any part of Ops Manager's state other than AppDB itself.
- Does not introduce cross-region or cross-cluster replication of AppDB.
- Does not change how backups are taken or restored for customer's own managed MongoDB databases.
- Customers referencing an existing MongoDB deployment as AppDB take on responsibility for that deployment's day-to-day management.
- No migration tool provided to ensure MongoDB resource configuration matches the internal AppDB config; users must manually specify configuration.
- **`PasswordSecretKeyRef` is not supported during forward migration.** The `AppDBSpec` might be completely empty when `ExternalApplicationDatabaseRef` is set, so reading `PasswordSecretKeyRef` is unreliable. A new password is generated instead. Customer-provided password secrets will be rotated.
- **`AutomationConfigOverride` is not supported** for external AppDB. The MongoDB CR does not have an equivalent field.
- **Multi-cluster topology is not supported in Phase 1.** `MongoDBMultiCluster` CRs are rejected at admission. `MongoDB` CRs with multi-cluster topology are also rejected.
- ~~**TLS/CA parity for external AppDB is WIP.** External mode currently does not configure `mms.mongoSSL` or `mms.mongoCA`. TLS-enabled external AppDBs using a private CA may fail connectivity until the TLS fix ships.~~ (Resolved in PR #1468 — TLS/CA is now resolved from the referenced MongoDB CR.)
- **1-2 minute downtime might occur during migration** if the configuration of the MongoDB/AppDB role CR differs from the internal AppDB configuration (different pod template, container set, etc.). If the configurations are semantically identical, no downtime should be present.

## Phase 2 — Multi-Cluster Support (Future)

Phase 2 will add support for `MongoDBMultiCluster` as the external AppDB. This requires:

- Adding `MongoDBMultiCluster` to the `ExternalApplicationDatabaseRef.Kind` enum
- Implementing multi-cluster topology validation for AppDB-role `MongoDBMultiCluster` CRs
- Cross-cluster ownership handoff (StatefulSet ownership across member clusters)
- Per-cluster finalizer state for safe reverse migration
- Shared password/keyfile distribution across member clusters
- Connection string computation for multi-cluster AppDB topology

## Open Questions

- ~~TLS/CA parity implementation details (WIP, shipping soon after base stack)~~ (Resolved in PR #1468)
- Feature usage tracking (telemetry)
