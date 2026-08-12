# External AppDB — Reverse Migration

This runbook migrates an Ops Manager from an **external** Application Database (a `MongoDB`
`role: AppDB` CR referenced via `spec.externalApplicationDatabaseRef`) **back to an
internally-managed** AppDB (`spec.applicationDatabase`), gracefully — the Ops Manager re-adopts the
existing AppDB StatefulSet, and the now-released `MongoDB` CR is deleted afterwards.

See also: [01-fresh-start](../01-fresh-start) and [02-forward-migration](../02-forward-migration).

## How it works

1. Starting state: the primary OM uses an external AppDB (this runbook first reaches that state, as
   in [01-fresh-start](../01-fresh-start)).
2. You remove `spec.externalApplicationDatabaseRef` and add `spec.applicationDatabase`. The operator
   asks the `MongoDB` CR to **release** the StatefulSet; the CR reports `Pending` (released/unmanaged).
   The operator then **re-adopts** the StatefulSet as an internally-managed AppDB. The connection
   string is unchanged (same hosts), so the OM pods do not roll.
3. After the handover the `MongoDB` CR is a leftover and is deleted.

## Configure & run

```bash
source env_variables.sh
./test.sh
```

Or step by step:

### Setup
1. `./code_snippets/03_0040_validate_env.sh`
2. `./code_snippets/03_0045_create_namespace.sh`
3. `./code_snippets/03_0100_install_operator.sh`
4. `./code_snippets/03_0200_create_om_admin_secret.sh`
5. `./code_snippets/03_0210_deploy_management_om.sh`
6. `./code_snippets/03_0215_wait_management_om.sh`
7. `./code_snippets/03_0220_create_appdb_project_configmap.sh`

### Reach the external-AppDB state
8. `./code_snippets/03_0300_create_appdb_mongodb.sh`
9. `./code_snippets/03_0305_wait_appdb.sh`
10. `./code_snippets/03_0310_create_primary_om.sh`
11. `./code_snippets/03_0315_wait_primary_om.sh`

### Reverse migration
12. `./code_snippets/03_0320_reconfigure_to_internal.sh` — remove `externalApplicationDatabaseRef`,
    add `applicationDatabase`.
13. `./code_snippets/03_0325_wait_release_and_adopt.sh` — CR is released (`Pending`); OM re-adopts the
    StatefulSet and its AppDB status returns to `Running`.
14. `./code_snippets/03_0330_delete_mongodb_cr.sh` — delete the released `MongoDB` CR.
15. `./code_snippets/03_0400_verify.sh` — StatefulSet owned by the OM; internal AppDB `Running`.

## Troubleshooting

- **`MongoDB` CR not released** — the OM must first request the release. Confirm the patch removed
  `externalApplicationDatabaseRef` (set to `null`) and added `applicationDatabase`.
- **Deleting the CR before the handover** — do not delete the `MongoDB` CR until step 13 shows the OM
  managing the internal AppDB again; deleting early (while it still owns the StatefulSet) would
  garbage-collect the AppDB. If you need the delete-first fallback path instead, see the operator
  documentation for recreate-from-retained-PVCs.
