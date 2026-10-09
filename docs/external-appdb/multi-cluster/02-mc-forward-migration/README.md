# Multi-cluster External AppDB — Forward Migration

This runbook migrates an Ops Manager from an **internally-managed multi-cluster AppDB**
(`spec.applicationDatabase` with `topology: MultiCluster`) to an **external**
`MongoDBMultiCluster` resource (`role: AppDB`) referenced via
`spec.externalApplicationDatabaseRef` (`kind: MongoDBMultiCluster`). The AppDB StatefulSets keep the
same names and hosts, so the connection string does not change and the OM does not roll.

See also: [01-mc-fresh-start](../01-mc-fresh-start) and [03-mc-reverse-migration](../03-mc-reverse-migration).
For the single-cluster equivalent, see [../../02-forward-migration](../../02-forward-migration).

> **Prerequisite — operator support.** Requires an operator build supporting
> `spec.externalApplicationDatabaseRef.kind: MongoDBMultiCluster` (CLOUDP-444251). The internal AppDB
> must already be `topology: MultiCluster`: the operator only allows switching to a `MongoDBMultiCluster`
> external AppDB when the current internal AppDB topology matches.

## How it works

1. The primary OM starts with an internal multi-cluster AppDB (`spec.applicationDatabase`, members
   spread across the member clusters, TLS-enabled).
2. A `MongoDBMultiCluster` (`role: AppDB`) named `<primary-om-name>-db` is created with the same
   topology/members/TLS. It cannot adopt the existing StatefulSets yet → reports `Pending` on the
   adoption gate.
3. Adding `spec.externalApplicationDatabaseRef` (`kind: MongoDBMultiCluster`) makes the OM detach its
   internal AppDB StatefulSets (per member cluster); the `MongoDBMultiCluster` CR adopts them and goes
   `Running`.

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
1–10. Same setup block as [01-mc-fresh-start](../01-mc-fresh-start) (`eadb-mc-02_0040` … `0220`):
validate env, create namespaces in every cluster, install the multi-cluster operator, admin secret,
cert-manager + CA + AppDB certificate, management OM, project ConfigMap.

### Before state
11. `eadb-mc-02_0300_create_primary_om_internal.sh` — primary OM with an internal multi-cluster AppDB.
12. `eadb-mc-02_0305_wait_internal_appdb.sh` — wait until the internal AppDB and OM are `Running`.

### Forward migration
13. `eadb-mc-02_0310_create_appdb_mongodbmulti.sh` — create the external `MongoDBMultiCluster`
    (`role: AppDB`, same name/topology).
14. `eadb-mc-02_0315_wait_appdb_pending.sh` — confirm it is `Pending` on the adoption gate.
15. `eadb-mc-02_0320_set_external_ref.sh` — patch the OM to add `externalApplicationDatabaseRef`.
16. `eadb-mc-02_0325_wait_after_switch.sh` — wait until the AppDB CR adopts and the OM is `Running`.
17. `eadb-mc-02_0400_verify.sh` — per-member StatefulSet ownership (`mongodbmulticluster` label) and
    cleared migration annotations.

## Troubleshooting

- **Switch rejected with a topology mismatch** — the internal AppDB must be `topology: MultiCluster`
  before switching to a `MongoDBMultiCluster` external AppDB.
- **AppDB CR stays `Pending` ("Cannot take ownership of the AppDB StatefulSet")** — expected until the
  OM detaches; it clears once `externalApplicationDatabaseRef` is set.
- **Ownership check** — multi-cluster AppDB StatefulSets are tracked by the `mongodbmulticluster` label
  per member cluster, not by `ownerReferences`.
