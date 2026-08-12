# External AppDB — Fresh Start

This runbook deploys an Ops Manager whose Application Database is an **external**
`MongoDB` resource (`role: AppDB`) referenced via `spec.externalApplicationDatabaseRef`,
starting from scratch (the primary Ops Manager never has an internally-managed AppDB).

See also: [02-forward-migration](../02-forward-migration) (move an existing internal AppDB to an
external one) and [03-reverse-migration](../03-reverse-migration) (move back to an internal AppDB).

## Topology

```
                 manages (project + credentials)
 management OM ───────────────────────────────► MongoDB (role: AppDB)   ← "<primary-om-name>-db"
                                                        ▲
                                                        │ externalApplicationDatabaseRef
                                                        │
                                                   primary OM  (no spec.applicationDatabase)
```

- **Management Ops Manager** — an ordinary Ops Manager (with its own internal AppDB) that *manages*
  the AppDB-role `MongoDB` resource, exactly like it would manage any other database project.
- **External AppDB** — a `MongoDB` replica set with `spec.role: AppDB`. **Its name must be
  `<primary-om-name>-db`** — the operator enforces this naming convention when resolving
  `externalApplicationDatabaseRef`.
- **Primary Ops Manager** — has **no** `spec.applicationDatabase`; it points at the external AppDB
  via `spec.externalApplicationDatabaseRef`. The operator resolves that `MongoDB` CR to compute the
  AppDB connection string (and its TLS/CA, if enabled) and never creates an internal AppDB
  StatefulSet for this OM. Its `status.applicationDatabase.phase` is reported as `Disabled`.

## Prerequisites

- A running Kubernetes cluster and the MongoDB Kubernetes operator's Helm chart access.
- `kubectl` and `helm` configured for your cluster.

## Configure

Edit the placeholder values, then source the environment:

```bash
source env_variables.sh
```

## Run

Run the whole runbook:

```bash
./test.sh
```

…or run the steps individually (in order):

### Setup

1. `./code_snippets/01_0040_validate_env.sh` — validate required environment variables.
2. `./code_snippets/01_0045_create_namespace.sh` — create the namespace.
3. `./code_snippets/01_0100_install_operator.sh` — install the operator via Helm.
4. `./code_snippets/01_0200_create_om_admin_secret.sh` — create the Ops Manager admin secret.
5. `./code_snippets/01_0210_deploy_management_om.sh` — deploy the management Ops Manager.
6. `./code_snippets/01_0215_wait_management_om.sh` — wait until it is `Running`; note its
   operator-provisioned API-key secret (used as the AppDB credentials).
7. `./code_snippets/01_0220_create_appdb_project_configmap.sh` — create the project ConfigMap
   pointing the AppDB CR at the management OM.

### Fresh Start

8. `./code_snippets/01_0300_create_appdb_mongodb.sh` — create the external AppDB `MongoDB`
   (`role: AppDB`, name `<primary-om-name>-db`).
9. `./code_snippets/01_0305_wait_appdb.sh` — wait until the AppDB is `Running`.
10. `./code_snippets/01_0310_create_primary_om.sh` — create the primary Ops Manager with
    `externalApplicationDatabaseRef` and no `applicationDatabase`.
11. `./code_snippets/01_0315_wait_primary_om.sh` — wait until the primary OM is `Running` and its
    AppDB status is `Disabled` (external AppDB is unmanaged by this OM).
12. `./code_snippets/01_0400_verify.sh` — verify pods, StatefulSet ownership and the connection-string
    secret.

## Troubleshooting

- **`externalApplicationDatabaseRef.name` rejected** — the referenced `MongoDB` name must be exactly
  `<primary-om-name>-db`.
- **AppDB `MongoDB` stuck `Pending` with "Cannot take ownership of the AppDB StatefulSet"** — a
  StatefulSet with that name already exists and is owned by another Ops Manager. In a true fresh
  start no such StatefulSet exists; this message belongs to the migration flows
  ([02](../02-forward-migration) / [03](../03-reverse-migration)).
- **Primary OM AppDB phase is `Disabled`, not `Running`** — expected: in external-AppDB mode the
  operator does not manage the AppDB, so it reports `Disabled`.
