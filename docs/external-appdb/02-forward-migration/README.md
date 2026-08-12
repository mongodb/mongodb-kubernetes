# External AppDB — Forward Migration

This runbook migrates an Ops Manager from an **internally-managed** Application Database
(`spec.applicationDatabase`) to an **external** one — a `MongoDB` resource (`role: AppDB`) referenced
via `spec.externalApplicationDatabaseRef` — without downtime and without rolling the OM pods.

See also: [01-fresh-start](../01-fresh-start) and [03-reverse-migration](../03-reverse-migration).

## How it works

1. The primary OM starts with an internal AppDB; the operator owns a StatefulSet named
   `<primary-om-name>-db`.
2. You create a `MongoDB` (`role: AppDB`) CR with **the same name**, managed by the management OM.
   It cannot adopt the existing StatefulSet yet — the operator's **adoption gate** keeps it `Pending`
   ("Cannot take ownership of the AppDB StatefulSet") while the StatefulSet is still owned by the OM.
3. You add `spec.externalApplicationDatabaseRef` to the primary OM. The operator **detaches** its
   internal AppDB StatefulSet (drops its OwnerReference, marks it migration-ready); the `MongoDB` CR
   then **adopts** it. Because the computed connection string is identical (same hosts), the OM pods
   do not roll.

## Configure & run

```bash
source env_variables.sh
./test.sh
```

Or step by step:

### Setup
1. `./code_snippets/02_0040_validate_env.sh`
2. `./code_snippets/02_0045_create_namespace.sh`
3. `./code_snippets/02_0100_install_operator.sh`
4. `./code_snippets/02_0200_create_om_admin_secret.sh`
5. `./code_snippets/02_0210_deploy_management_om.sh`
6. `./code_snippets/02_0215_wait_management_om.sh`
7. `./code_snippets/02_0220_create_appdb_project_configmap.sh`

### Migration
8. `./code_snippets/02_0300_create_primary_om_internal.sh` — primary OM with an internal AppDB.
9. `./code_snippets/02_0305_wait_internal_appdb.sh` — wait until Running.
10. `./code_snippets/02_0310_create_appdb_mongodb.sh` — create the `MongoDB` (`role: AppDB`) CR
    (name `<primary-om-name>-db`).
11. `./code_snippets/02_0315_wait_appdb_pending.sh` — confirm it is `Pending` on the adoption gate.
12. `./code_snippets/02_0320_set_external_ref.sh` — add `externalApplicationDatabaseRef` to the OM.
13. `./code_snippets/02_0325_wait_after_switch.sh` — AppDB CR adopts and becomes Running; OM Running.
14. `./code_snippets/02_0400_verify.sh` — StatefulSet now owned by the CR; migration annotation cleared.

## Troubleshooting

- **AppDB CR stays `Pending` after the switch** — the operator must first detach the StatefulSet.
  Confirm `spec.externalApplicationDatabaseRef` was applied to the primary OM and that the referenced
  name equals `<primary-om-name>-db`.
- **OM pods rolled during the switch** — indicates the computed connection string changed. For the
  default-port replica-set case it should be identical; non-default ports are out of scope.
