# External AppDB — API (CRD) changes

This document describes the CRD/API additions that enable an **external Application Database** — an
Ops Manager whose AppDB is a separate, operator-managed `MongoDB` resource instead of the
internally-managed one. It covers the new spec fields, their validation rules, and the annotations
the operator uses to coordinate the migration flows.

## Summary

| Kind | Field / annotation | Type | Status |
|---|---|---|---|
| `MongoDBOpsManager` | `spec.externalApplicationDatabaseRef` | object | **new** |
| `MongoDBOpsManager` | `spec.applicationDatabase` | object | **now optional** (was effectively required) |
| `MongoDB` | `spec.role` | string enum | **new** |
| StatefulSet | `mongodb.com/appdb-migration-ready` | annotation | **new** (forward migration) |
| StatefulSet | `mongodb.com/appdb-reverse-migration-ready` | annotation | **new** (reverse migration) |

## `MongoDBOpsManager` CRD

### New: `spec.externalApplicationDatabaseRef`

References a `MongoDB` resource to use as this Ops Manager's AppDB instead of an internally-managed
one.

```yaml
spec:
  externalApplicationDatabaseRef:
    name: <om-name>-db   # required
    kind: MongoDB        # required; enum: MongoDB
```

Fields:

| Field | Required | Notes |
|---|---|---|
| `name` | yes | Name of the `MongoDB` resource to use as the external AppDB. Must be in the **same namespace** as the `MongoDBOpsManager`, and named **`<MongoDBOpsManager-name>-db`** (operator-enforced). |
| `kind` | yes | Kind of the referenced resource. Enum — currently only `MongoDB`. |

Semantics / validation:

- **Precedence:** when both `externalApplicationDatabaseRef` and `applicationDatabase` are set, the
  external reference **takes precedence**; the operator does not manage an internal AppDB for this OM.
- **Naming rule:** the referenced name must equal `<om-name>-db`. This is what lets the OM and the
  `MongoDB` CR converge on the **same** AppDB StatefulSet name (required for the migration/adoption
  handover).
- **Same namespace:** cross-namespace references are not allowed.
- When the external reference is set, the AppDB-specific validations that normally run against
  `spec.applicationDatabase` are skipped (there is no internal AppDB spec to validate).
- There is an internal, non-serialized `Namespace` field on the struct (`json:"-"`) the operator
  populates at runtime from the OM's namespace; it is **not** part of the CRD/API surface.

### Changed: `spec.applicationDatabase` is now optional

`applicationDatabase` may now be **omitted entirely** when `externalApplicationDatabaseRef` is set. It
remains the way to configure a normal, internally-managed AppDB otherwise. If both are present the
external reference wins and `applicationDatabase` is ignored.

### Behavioral note (status)

For an OM using an external AppDB, `status.applicationDatabase.phase` is reported as **`Disabled`**
(the operator does not manage an internal AppDB for it) — `Disabled` here is expected, not an error.
`status.opsManager.phase` behaves as usual.

## `MongoDB` CRD

### New: `spec.role`

Marks a `MongoDB` (or `MongoDBMultiCluster`) resource as playing a special role for another resource.

```yaml
spec:
  role: AppDB          # enum: AppDB (only supported value)
```

| Field | Notes |
|---|---|
| `role` | Optional string, enum `AppDB`. Marks this `MongoDB` as the externally-managed Application Database for a `MongoDBOpsManager`. When unset, the resource is an ordinary MongoDB deployment. |

Validation / immutability:

- Enum-restricted to `AppDB` (the only supported value today).
- **Immutable:** you cannot add `role: AppDB` to an existing plain `MongoDB`, nor remove it from one
  that has it — the value must be set at creation and left unchanged (the migration flows create/
  delete the CR rather than toggling the field).
- A `role: AppDB` resource is a normal replica set that registers as a project on a **management**
  Ops Manager (via `spec.opsManager.configMapRef` + `spec.credentials`); its `spec.version` must be a
  MongoDB version that OM offers.

## Annotations (on the AppDB StatefulSet)

These coordinate the hand-off of the shared AppDB StatefulSet between a `MongoDBOpsManager` (internal
AppDB) and a `MongoDB` (`role: AppDB`) CR during migrations. They live on the **StatefulSet**, are set
and cleared by the operator, and are **not** meant to be edited by users.

| Annotation | Direction | Set by | Meaning / lifecycle |
|---|---|---|---|
| `mongodb.com/appdb-migration-ready` | Forward (internal → external) | OM (AppDB) reconciler | Marks the internal AppDB StatefulSet as **fully detached** from the `MongoDBOpsManager` (OwnerReference dropped), signalling that the referenced `MongoDB` CR may now **adopt** it (the CR's adoption gate waits for this). Cleared once the CR has adopted the StatefulSet. |
| `mongodb.com/appdb-reverse-migration-ready` | Reverse (external → internal) | internal AppDB reconciler | The **release request**: set on a StatefulSet still owned by the `MongoDB` CR to ask it to release ownership. The `MongoDB` controller answers by stripping its OwnerReference; the annotation is removed at **adoption** by the OM (not at migration completion), symmetric with the forward direction — from adoption onward the OwnerReference is the authoritative state. |

In both directions the AppDB StatefulSet keeps the **same name** (`<om-name>-db`) and the same member
hosts, so the computed AppDB connection string does not change and the OM pods do not roll during the
switch.

## Related resources (unchanged shapes, noted for completeness)

- **Project ConfigMap** — the `role: AppDB` MongoDB points at the management OM through the usual
  `spec.opsManager.configMapRef` (a ConfigMap with `baseUrl`, `projectName`, `orgId`).
- **Credentials** — `spec.credentials` references the management OM's programmatic API-key secret
  (operator-provisioned, named `<namespace>-<management-om-name>-admin-key`).
- **Connection-string secret** — the operator computes the AppDB connection-string secret
  (`<om-name>-db-connection-string`) for the primary OM, exactly as for an internal AppDB.

## See also

- [01-fresh-start](./01-fresh-start) — deploy external AppDB from scratch.
- [02-forward-migration](./02-forward-migration) — internal → external (uses `appdb-migration-ready`).
- [03-reverse-migration](./03-reverse-migration) — external → internal (uses
  `appdb-reverse-migration-ready`).
</content>
