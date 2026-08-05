# Technical Design: Database in MongoDBUser Connection String URI Path

August 2026

Filip-Andrei Cirtog

KUBE-121, related: CLOUDP-382956, HELP-94898

## Background

The `MongoDBUser` custom resource lets customers declare a database user and have the
operator generate a Kubernetes `Secret` containing ready-to-use connection strings
(`connectionString.standard` and `connectionString.standardSrv`). Customers configure the
user's database via `spec.db`.

`spec.db` is used for two logically distinct purposes in a MongoDB connection string:

1. The `authSource` query parameter, which tells the driver which database to
   authenticate against.
2. The database segment of the URI path, which tells the driver which database to use by
   default when the application does not explicitly call `.db("something")`.

Historically the operator only used `spec.db` for the first purpose. CLOUDP-382956 fixed
a bug where `authSource` was missing entirely. The second purpose was never implemented:
the generated connection string always had an empty path segment (`.../?authSource=...`),
so any driver defaulted to the `test` database instead of the one the customer actually
configured.

This was reported by a customer in HELP-94898: applications relying on the connection
string to select the default database connected to `test` instead of their intended
database, forcing them to either hardcode the database name in application code or
manually patch the generated `Secret`, defeating the purpose of the operator managing
the connection string for them.

Because `spec.db` currently drives both `authSource` and the URI path identically, and
because some customers intentionally want a user to authenticate against one database
while defaulting into another (or not want the path populated at all, for example if the
user should not have implicit access to the database named in `spec.db`), simply always
writing `spec.db` into the path is not a safe default for every existing user of the
operator. This was raised directly in the HELP ticket thread and is why the fix was
deferred to a major version bump instead of shipping as a patch release.

## Goals

- Populate the database segment of the generated connection string URI path so drivers
  default into the correct database, matching customer expectations and the MongoDB
  connection string URI format.
- Introduce `spec.authSource` and `spec.defaultDatabase` as the two new, independently
  configurable fields, replacing the single overloaded `spec.db`.
- Preserve `spec.db` as a deprecated field so existing `MongoDBUser` resources continue
  to work without modification: `spec.db` alone will continue to drive both `authSource`
  and the URI path exactly as before.
- Validate that customers cannot combine `spec.db` with the new fields, and that
  `spec.authSource` / `spec.defaultDatabase` are always set together.
- Update the generated `AutomationConfig` user database, `MongoDBUser` status database,
  and the connection string secret naming scheme to use the resolved (effective) auth
  database rather than the raw `spec.db` field.

## Non-Goals

- Automatically migrating existing `MongoDBUser` resources from `spec.db` to the new
  fields. Customers who want the new independent control must opt in explicitly.
- Removing `spec.db` entirely. It remains supported indefinitely as the deprecated,
  simpler path for customers who don't need `authSource` and the URI path to diverge.
- Changing behavior for the `MongoDBUser` type in the Community Operator's embedded user
  spec beyond mirroring the same field split, since it is a logically separate CRD/type
  even though the connection string construction code is shared via `authtypes.User`.
- Adding a flag to disable populating the path when `spec.db` (the deprecated field) is
  used alone. This was suggested by the reporting SE in HELP-94898 as a way to avoid the
  breaking change for customers who don't want the path populated. We decided against it:
  customers who need the two behaviors to diverge already have an explicit path via the
  new fields, so a separate opt-out flag on the deprecated field would just be redundant
  surface area. Customers who don't adopt the new fields accept the documented, major
  version breaking change instead.

## Architecture Sketch

```
MongoDBUserSpec
  ├── Database        string  (deprecated, json:"db,omitempty")
  ├── AuthSource       string  (json:"authSource,omitempty")
  └── DefaultDatabase  string  (json:"defaultDatabase,omitempty")

MongoDBUserSpec.EffectiveAuthDatabase() string
  returns spec.db if set, otherwise spec.authSource

MongoDBUserSpec.EffectivePathDatabase() string
  returns spec.db if set, otherwise spec.defaultDatabase

MongoDBUserSpec.ValidateSpec() error
  rejects spec.db combined with the new fields
  rejects authSource set without defaultDatabase (and vice versa)
```

`connectionstring.ConnectionStringBuilder.BuildConnectionString` is extended to accept
both the resolved `authSource` and the resolved path database explicitly, rather than
deriving `authSource` implicitly from the authentication mode and never populating the
path:

```go
type ConnectionStringBuilder interface {
    BuildConnectionString(userName, password, authSource, defaultDatabase string, scheme Scheme, connectionParams map[string]string) string
}
```

The builder writes `defaultDatabase` into the URI path (`PathDatabase()` strips it back
to empty for the `$external` pseudo-database, which must never appear in the path), and
uses an explicit `authSource` when provided, falling back to the previous
authentication-mode-based default only when the caller does not supply one.

## Reading Path: `MongoDBUser` Reconcile Flow

Every place in the reconciler that previously read `user.Spec.Database` directly is
switched to `user.Spec.EffectiveAuthDatabase()` (for authentication purposes: building
the `AutomationConfig` user, `ChangedIdentifier()`, updating `Status.Database`, secret
naming) or `user.Spec.EffectivePathDatabase()` (for the connection string URI path).

For a `MongoDBUser` that only sets `spec.db`, both helpers return `spec.db`, so the
resolved authentication database is byte-for-byte identical to the pre-change behavior.
The only observable difference for these users is that the URI path, which used to
always be empty, is now populated with `spec.db` too. This is the deliberate, disclosed
breaking change.

For a `MongoDBUser` that sets neither field, the reconciler defaults `spec.db` to
`"admin"` before validation runs, matching the historical default.

## Validation

```go
func (spec MongoDBUserSpec) ValidateSpec() error {
    usingLegacy := spec.Database != ""
    usingNew := spec.AuthSource != "" || spec.DefaultDatabase != ""
    if usingLegacy && usingNew {
        return fmt.Errorf("spec.db is deprecated and cannot be combined with spec.authSource or spec.defaultDatabase")
    }
    if !usingLegacy && spec.AuthSource != "" && spec.DefaultDatabase == "" {
        return fmt.Errorf("spec.defaultDatabase is required when spec.authSource is set")
    }
    if !usingLegacy && spec.DefaultDatabase != "" && spec.AuthSource == "" {
        return fmt.Errorf("spec.authSource is required when spec.defaultDatabase is set")
    }
    return nil
}
```

This runs at the top of `Reconcile()`, after the default-fill step, and transitions the
`MongoDBUser` to a `Failed`/`Invalid` phase with a clear message rather than silently
picking one field over the other.

## CRD Changes

`spec.db` becomes optional (previously required, alongside `username`). `authSource` and
`defaultDatabase` are added as new optional string fields. `username` remains required;
an earlier draft of this change accidentally marked it optional too, which was caught
during review and reverted, since dropping that requirement was unintentional and
unrelated to the `spec.db` migration.

## Why not reuse `spec.db` for the path and add an opt-out flag instead?

This was the customer's (via the supporting SE, Domenico Foglia) original suggestion in
HELP-94898: have `spec.db` populate the path by default, with an optional setting to
disable that behavior for the edge case where a user should not have implicit access to
the database named in `spec.db`.

We considered it, but decided against it for two reasons:

1. It still couples the two concerns (`authSource` and default database) into a single
   field with a side-channel toggle, rather than letting them vary independently, which
   is the more common shape of the underlying problem (a user may need to authenticate
   against one database but default into a different one entirely, not just "populate
   the path or don't").
2. Splitting into two explicit fields, `authSource` and `defaultDatabase`, means the
   `MongoDBUser` spec directly documents intent, and no separate boolean flag needs to be
   introduced, discovered, or explained to customers who want the new behavior.

**Decision:** introduce `spec.authSource` and `spec.defaultDatabase` as independent
fields and deprecate `spec.db`, rather than adding an opt-out flag alongside `spec.db`.

## Why is this a breaking change requiring a major version?

As flagged internally on HELP-94898 by Vinicius Nunes Lage: populating the URI path
changes the default database every existing `MongoDBUser`-generated connection string
resolves to, for every customer already relying on the empty-path (`test` database)
behavior, whether they noticed it or not. This is exactly the kind of change that must
ride along with a major version bump (MCK 2.0) rather than ship silently in a patch
release, so customers have a clear signal to review their `MongoDBUser` resources before
upgrading.

## Testing Plan

- Unit tests for `EffectiveAuthDatabase()` / `EffectivePathDatabase()` covering the
  legacy-only, new-fields-only, and neither-set cases.
- Unit tests for `ValidateSpec()` covering all four combinations of legacy/new field
  usage, valid and invalid.
- Unit tests for `ConnectionStringBuilder.BuildConnectionString` covering: database
  appears in the URI path, empty database produces an empty path segment, `$external`
  is never written into the path, explicit `authSource` takes precedence over the
  authentication-mode-derived default.
- Reconciler-level tests confirming the `AutomationConfig` user, `MongoDBUserStatus`, and
  generated connection string `Secret` all resolve to the same values for a `spec.db`-only
  user as they did before this change, aside from the intentional path change.
- Audited every other caller of `mongodbUser.Spec.AuthSource` / `.Spec.Database`
  in the codebase (notably `getS3MongoDbUserNameAndPassword` in the Ops Manager
  controller, used for building the S3 backup datastore connection string) to confirm
  they were switched to the effective helpers and not left reading the deprecated field
  directly, which would silently break `spec.db`-only users on that code path.

## References

- KUBE-121: Connection string secret is missing database in URI path
- CLOUDP-382956: MongoDBUser connection string doesn't set authSource parameter
- HELP-94898: [MCK] MongoDBUser connection string secret missing database name in URI path
- MongoDB Connection String URI Format documentation
