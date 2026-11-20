# Multi-cluster External AppDB — Reverse Migration

This runbook migrates an Ops Manager from an **external** `MongoDBMultiCluster` AppDB
(`role: AppDB`, referenced via `spec.externalApplicationDatabaseRef`, `kind: MongoDBMultiCluster`)
back to an **internally-managed multi-cluster AppDB** (`spec.applicationDatabase` with
`topology: MultiCluster`). The AppDB StatefulSets keep the same names and hosts, so the connection
string does not change.

See also: [01-mc-fresh-start](../01-mc-fresh-start) and [02-mc-forward-migration](../02-mc-forward-migration).
For the single-cluster equivalent, see [../../03-reverse-migration](../../03-reverse-migration).

> **Prerequisite — operator support.** Requires an operator build supporting
> `spec.externalApplicationDatabaseRef.kind: MongoDBMultiCluster` (CLOUDP-444251).

## How it works

1. Reach the external-AppDB state (same as [01-mc-fresh-start](../01-mc-fresh-start)): management OM +
   external `MongoDBMultiCluster` AppDB + primary OM referencing it.
2. Patch the primary OM to remove `spec.externalApplicationDatabaseRef` and add back
   `spec.applicationDatabase` (`topology: MultiCluster`, same members/TLS). The operator asks the
   `MongoDBMultiCluster` CR to release the StatefulSets, then re-adopts them as an internal AppDB.
3. The released CR reports `Pending` ("other owner") and is deleted; its StatefulSets survive and are
   now managed by the OM.

## Configure

```bash
source env_variables.sh
```

## Run

```bash
./test.sh
```

…or step by step (in order):

### Setup
1–10. Same setup block as [01-mc-fresh-start](../01-mc-fresh-start) (`eadb-mc-03_0040` … `0220`).

### Reach the external-AppDB state
11. `eadb-mc-03_0300_create_appdb_mongodbmulti.sh` — external `MongoDBMultiCluster` (`role: AppDB`).
12. `eadb-mc-03_0305_wait_appdb.sh` — wait until the AppDB is `Running`.
13. `eadb-mc-03_0310_create_primary_om.sh` — primary OM with `externalApplicationDatabaseRef`.
14. `eadb-mc-03_0315_wait_primary_om.sh` — wait until the OM is `Running` / AppDB `Disabled`.

### Reverse migration
15. `eadb-mc-03_0320_reconfigure_to_internal.sh` — patch the OM: drop the ref, add
    `applicationDatabase` (`topology: MultiCluster`).
16. `eadb-mc-03_0325_wait_release_and_adopt.sh` — the CR becomes `Pending` (released); the OM re-adopts
    the internal AppDB and becomes `Running`.
17. `eadb-mc-03_0330_delete_mongodbmulti_cr.sh` — delete the released CR.
18. `eadb-mc-03_0400_verify.sh` — per-member StatefulSets survive (empty `ownerReferences`), OM internal
    AppDB `Running`.

## Troubleshooting

- **Restored `applicationDatabase` must be `topology: MultiCluster`** with the same member layout and
  TLS as the external AppDB, so the OM can re-adopt the existing multi-cluster StatefulSets.
- **CR stuck `Pending` ("other owner")** — expected after reconfiguring; the CR is meant to be deleted
  once the OM has re-adopted the StatefulSets.
