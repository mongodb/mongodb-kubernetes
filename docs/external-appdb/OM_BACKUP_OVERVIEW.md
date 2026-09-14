# Ops Manager Backup — overview

## What it is

**Ops Manager (OM) Backup** provides continuous, application-consistent backups of the MongoDB
deployments that Ops Manager manages. Instead of periodic `mongodump` snapshots taken from outside,
OM keeps a live copy of your data and a stream of oplog entries, which lets you:

- **Restore a full snapshot** of a replica set or sharded cluster to a new or existing deployment.
- **Restore to any point in time** (PITR) between snapshots, by replaying the oplog up to a chosen
  timestamp — useful to recover from an accidental drop/delete.
- **Query a backup without restoring it** (queryable backups).
- Meet retention/compliance requirements, optionally with **immutable** (WORM) backups.

## Why you need it

- **Disaster recovery** — bring a deployment back after data loss, corruption, or a bad migration.
- **Operational safety net** — roll back an erroneous bulk write to the exact moment before it.
- **Lower RPO/RTO** than snapshot-only strategies, thanks to continuous oplog capture (PITR).
- **Centralized management** — backup policies, snapshot schedules and restores are driven through
  Ops Manager for every project it manages, rather than per-database tooling.

## How it works with the operator

When you enable Backup on a `MongoDBOpsManager` resource, the operator provisions and wires together
these components:

```
   MongoDB deployments (agents)                  Ops Manager application
            │  snapshots + oplog                          │  policies, schedules, restores
            ▼                                              ▼
      ┌──────────────┐        head DB (staging)     ┌──────────────────┐
      │ Backup Daemon │◄──────────────────────────► │  snapshot store   │  (Blockstore or S3)
      │  StatefulSet  │                              │  + oplog store    │  (PITR)
      └──────────────┘                              └──────────────────┘
```

- **Backup Daemon** — a dedicated StatefulSet (separate from the OM application pods) that runs the
  backup jobs: it maintains a local **head database** (staging copy of each backed-up deployment) and
  writes snapshots to the configured store. Its state is reported under
  `status.backup.phase` on the `MongoDBOpsManager` resource.
- **Snapshot store** — where full snapshots live. Either an in-cluster **Blockstore** (a MongoDB
  database) or an **S3-compatible bucket**.
- **Oplog store** — a MongoDB database that stores the oplog slices enabling point-in-time restores.
  Can also be backed by S3 (`s3OpLogStores`).
- **Metadata** lives in the OM Application Database (AppDB).

## Enabling Backup

Backup is configured under `spec.backup` of the `MongoDBOpsManager` resource. Minimal shape:

```yaml
apiVersion: mongodb.com/v1
kind: MongoDBOpsManager
metadata:
  name: ops-manager
spec:
  version: 8.0.7
  applicationDatabase:
    members: 3
    version: 8.0.5-ent
  backup:
    enabled: true          # provisions the Backup Daemon StatefulSet
    members: 1             # number of Backup Daemon pods
    # --- snapshot store: pick ONE style ---
    blockStores:           # in-cluster MongoDB blockstore(s)
      - name: block-store
        mongodbResourceRef:
          name: backup-blockstore-db
    # or S3 snapshot store:
    # s3Stores:
    #   - name: s3-snapshot
    #     s3BucketName: my-om-snapshots
    #     s3BucketEndpoint: s3.amazonaws.com
    #     s3SecretRef: { name: s3-creds }
    # --- oplog store (required for point-in-time restore) ---
    opLogStores:
      - name: oplog-store
        mongodbResourceRef:
          name: backup-oplog-db
    # or S3-backed oplog: s3OpLogStores: [ ... ]
```

Key `spec.backup` fields (all optional unless noted):

| Field | Purpose |
|---|---|
| `enabled` | Turns Backup on and provisions the Backup Daemon. |
| `members` | Number of Backup Daemon pods. |
| `headDB` | Persistence config for the daemon's local head database (size/StorageClass). |
| `blockStores` | In-cluster MongoDB **snapshot** store(s). |
| `s3Stores` | S3-compatible **snapshot** store(s). |
| `opLogStores` | MongoDB **oplog** store(s) — enables point-in-time restore. |
| `s3OpLogStores` | S3-backed oplog store(s). |
| `encryption.kmip` | KMIP-based backup encryption. |
| `assignmentLabels` | Labels that steer which deployments a daemon/store services. |
| `queryableBackupSecretRef` | PEM secret enabling queryable (restore-less) backups. |

For S3 stores you can enable **S3 Object Lock** for immutable backups, and (when the store's TLS is
served by the same CA as the AppDB) reuse the AppDB CA as the S3 CA instead of supplying a separate
one.

## Backup and the external AppDB

Backup metadata is stored in the AppDB, and both the OM application **and the Backup Daemon** connect
to it. When the AppDB is an **external** `MongoDB` (`role: AppDB`) referenced through
`spec.externalApplicationDatabaseRef` (see the [external-appdb runbooks](./README.md)):

- The operator resolves the referenced `MongoDB` CR's TLS/CA and threads it into **both** the OM
  StatefulSet **and** the Backup Daemon StatefulSet, so the daemon trusts a TLS-enabled external
  AppDB exactly as it would an internal one.
- Enabling Backup is unchanged — you still set `spec.backup` on the primary OM. The only difference
  is where the AppDB lives; the Backup Daemon connects using the same operator-computed AppDB
  connection string (and CA) that the OM uses.

## Verifying

```bash
# Backup Daemon rollout state:
kubectl get om <om-name> -n <ns> -o jsonpath='{.status.backup.phase}{"\n"}'   # -> Running when ready

# Backup Daemon StatefulSet and pods:
kubectl get statefulset,pods -n <ns> -l app=<om-name>-backup-daemon
```

Once `status.backup.phase` is `Running`, configure snapshot schedules and run restores from the Ops
Manager UI (or its API) for any project OM manages.

## Notes & caveats

- **Oplog store is required for point-in-time restores.** With only a snapshot store you can restore
  whole snapshots but not to an arbitrary timestamp.
- **Storage sizing** — the head DB and snapshot store grow with your data and retention; size
  `headDB` and the store databases/buckets accordingly.
- **Disabling Backup** (`enabled: false`) removes the Backup Daemon but does not delete data already
  written to external stores (S3 buckets, blockstore databases).
</content>
