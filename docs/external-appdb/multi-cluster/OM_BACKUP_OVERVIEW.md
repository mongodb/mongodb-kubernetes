# Ops Manager Backup in multi-cluster deployments — overview

This is the multi-cluster companion to [../OM_BACKUP_OVERVIEW.md](../OM_BACKUP_OVERVIEW.md). It assumes
you already know what OM Backup is (snapshots + oplog, PITR, queryable/immutable backups) and focuses
on what changes when the topology spans **multiple Kubernetes clusters**.

There are two independent "multi-cluster" axes — you can use either or both:

1. **A multi-cluster Ops Manager** — the OM application *and its Backup Daemons* are spread across
   clusters (`spec.topology: MultiCluster`, `spec.clusterSpecList`).
2. **A multi-cluster workload being backed up** — a `MongoDBMultiCluster` replica set whose members
   live in several clusters, backed up by an OM (the OM itself may be single- or multi-cluster).

## Why multi-cluster backup

- **Survive a cluster/region outage** — spread the OM app, the Backup Daemon and the backing stores so
  a single cluster failure does not take backups down with it.
- **Back up multi-cluster databases** — a `MongoDBMultiCluster` deployment is backed up the same way a
  single-cluster one is: agents in every member cluster stream snapshots and oplog to the Backup
  Daemon.
- Everything else (PITR, queryable backups, S3 Object Lock / immutability, retention) works exactly
  as in the [single-cluster overview](../OM_BACKUP_OVERVIEW.md).

## How it works with the operator (multi-cluster OM)

On a `topology: MultiCluster` Ops Manager, the Backup Daemon is **per member cluster**: the operator
creates a distinct Backup Daemon StatefulSet and headless service in each cluster whose
`clusterSpecList` entry requests daemon members. The **stores stay global** — `opLogStores`,
`blockStores`, `s3Stores`, `s3OpLogStores` and `headDB` are configured once under top-level
`spec.backup` and shared by all daemons.

```
 cluster-0                    cluster-1                    cluster-2 (daemon-only)
 ┌────────────────┐          ┌────────────────┐          ┌────────────────────┐
 │ OM app pod     │          │ OM app pod     │          │ Backup Daemon pod  │
 │ members: 1     │          │ members: 1     │          │ backup.members: 1  │
 │ backup.members:0│         │ backup.members:0│         │ members: 0 (no app)│
 └────────────────┘          └────────────────┘          └─────────┬──────────┘
          └──────────────── shared stores (top-level spec.backup) ──┘
                 opLogStores / blockStores / s3Stores / s3OpLogStores / headDB
```

- **Per-cluster Backup Daemon count** = `spec.clusterSpecList[<i>].backup.members`. (The top-level
  `spec.backup.members` is used for single-cluster OM; in MultiCluster the per-cluster value wins.)
- A common pattern is a **dedicated daemon cluster**: `members: 0` (no OM app pod) with
  `backup.members: 1` — see the example below.
- Per-cluster status is reported under `status.backup` (the operator fills
  `status.backup.clusterStatusList` with `{clusterName, replicas}` per cluster).

## Enabling Backup on a multi-cluster Ops Manager

```yaml
apiVersion: mongodb.com/v1
kind: MongoDBOpsManager
metadata:
  name: ops-manager
spec:
  version: 8.0.7
  topology: MultiCluster
  clusterSpecList:
    - clusterName: cluster-0
      members: 1                 # OM application pods here
      backup:
        members: 0               # no Backup Daemon on this cluster
    - clusterName: cluster-1
      members: 1
      backup:
        members: 0
    - clusterName: cluster-2
      members: 0                 # no OM app pod — daemon-only cluster
      backup:
        members: 1               # Backup Daemon runs here
  applicationDatabase:
    topology: MultiCluster
    version: 8.0.5-ent
    clusterSpecList:
      - clusterName: cluster-0
        members: 3
      - clusterName: cluster-1
        members: 2
  backup:
    enabled: true                # provisions the per-cluster Backup Daemon(s)
    # --- stores are GLOBAL (top-level), shared by all daemons ---
    s3Stores:                    # S3 snapshot store
      - name: my-s3-block-store
        s3SecretRef:
          name: s3-access-secret
        s3BucketName: my-om-snapshots
        s3BucketEndpoint: s3.amazonaws.com
        pathStyleAccessEnabled: true
        # customCertificateSecretRefs: [ { name: s3-ca-cert, key: ca.crt } ]
        # objectLockEnabled: true     # immutable backups (OM version-gated)
    s3OpLogStores:               # S3-backed oplog store (PITR)
      - name: my-s3-oplog-store
        s3SecretRef:
          name: s3-access-secret
        s3BucketName: my-om-oplog
        s3BucketEndpoint: s3.amazonaws.com
        pathStyleAccessEnabled: true
    # Or MongoDB-backed stores instead of S3:
    # opLogStores:
    #   - name: oplog-store
    #     mongodbResourceRef: { name: backup-oplog-db }
    #     mongodbUserRef: { name: backup-oplog-user }   # required for SCRAM-auth stores
    # blockStores:
    #   - name: block-store
    #     mongodbResourceRef: { name: backup-blockstore-db }
```

### Key fields (multi-cluster specifics)

| Field | Purpose |
|---|---|
| `spec.topology: MultiCluster` | Required for a multi-cluster OM; makes `clusterSpecList` mandatory. |
| `spec.clusterSpecList[].members` | OM **application** pods in that cluster. |
| `spec.clusterSpecList[].backup.members` | **Backup Daemon** pods in that cluster (per-cluster override of `spec.backup`). |
| `spec.clusterSpecList[].backup.headDB` / `statefulSet` | Per-cluster daemon head-DB persistence / pod overrides. |
| `spec.backup.enabled` | Turns Backup on. With it, **at least one** `clusterSpecList` item must set `backup.members > 0` (operator validation). |
| `spec.backup.opLogStores` / `s3OpLogStores` | Oplog store(s) — required for point-in-time restore. |
| `spec.backup.blockStores` / `s3Stores` | Snapshot store(s) — in-cluster MongoDB or S3. |
| `spec.backup.headDB` | Global default head-DB persistence for the daemons. |
| `s3Stores[].objectLockEnabled` | Immutable (WORM) backups; allowed only on OM versions that support it. |

## Backing up a multi-cluster MongoDB workload

A `MongoDBMultiCluster` replica set is backed up like any other deployment — enable it on the workload
and the agents in every member cluster stream to the Backup Daemon:

```yaml
apiVersion: mongodb.com/v1
kind: MongoDBMultiCluster
metadata:
  name: my-mc-rs
spec:
  type: ReplicaSet
  version: 8.0.5-ent
  opsManager:
    configMapRef:
      name: my-project
  credentials: om-credentials
  backup:
    mode: enabled               # turn on backup for this multi-cluster workload
  clusterSpecList:
    - clusterName: cluster-1
      members: 2
    - clusterName: cluster-2
      members: 1
```

The critical requirement is that **agents in each member cluster can reach Ops Manager** to register
and stream backup data. How that is wired depends on connectivity:

- **Service mesh (Istio):** members resolve the OM service across clusters over the mesh. The only
  extra wiring in practice is making the OM reachable under a stable URL (e.g. mapping the OM
  external/LB address to an `*.interconnected` hostname and setting `mms.centralUrl` accordingly).
- **No mesh:** each member is exposed via external addresses — set per-cluster
  `clusterSpecList[].externalAccess.externalDomain` on the workload and point OM at an external URL
  (`spec.opsManagerURL` / `externalConnectivity`). The Backup Daemon, OM and the cross-cluster mongod
  processes must all resolve each other's **external** hostnames (DNS/external-dns).

## Stores: topology notes

- The backing **oplog/blockstore databases are ordinary MongoDB replica sets** (not necessarily
  multi-cluster) and typically run on the central cluster; reference them with
  `mongodbResourceRef` (and `mongodbUserRef` when they use SCRAM auth).
- **S3 stores** need no in-cluster database. For cross-region durability, prefer S3 (`s3Stores` +
  `s3OpLogStores`), optionally with `objectLockEnabled` for immutability.
- A store must be reachable from **every** Backup Daemon, wherever the daemons run.

## Backup with an external multi-cluster AppDB

Backup metadata lives in the AppDB, and both the OM application **and the Backup Daemon** connect to
it. When the AppDB is an external `MongoDBMultiCluster` (`role: AppDB`) referenced via
`spec.externalApplicationDatabaseRef` (see the [multi-cluster external-AppDB runbooks](./README.md)):

- The operator resolves the referenced CR's TLS/CA and threads it into **both** the OM StatefulSet(s)
  **and** the Backup Daemon StatefulSet(s), so the daemons trust the TLS-enabled external AppDB.
- Enabling Backup is unchanged — you still set `spec.backup` on the primary OM; the daemons use the
  same operator-computed AppDB connection string and CA.
- The external-AppDB TLS CA must include the public roots for `downloads.mongodb.com` (same as for the
  AppDB agents) so the Backup Daemon can download its MongoDB binary — see
  [01-mc-fresh-start/WALKTHROUGH.md](./01-mc-fresh-start/WALKTHROUGH.md) step 5 and its Troubleshooting.

## Verifying

```bash
# Overall backup phase (Running when the daemon(s) are ready):
kubectl get om <om-name> --context <central-ctx> -n <ns> \
  -o jsonpath='{.status.backup.phase}{"\n"}'

# Per-cluster Backup Daemon replicas reported for a multi-cluster OM:
kubectl get om <om-name> --context <central-ctx> -n <ns> \
  -o jsonpath='{range .status.backup.clusterStatusList[*]}{.clusterName}={.replicas}{"\n"}{end}'

# Backup Daemon StatefulSet/pods in a daemon cluster:
kubectl get statefulset,pods --context <daemon-ctx> -n <ns> -l app=<om-name>-backup-daemon
```

Once `status.backup.phase` is `Running`, configure snapshot schedules and run restores (including
PITR) from the Ops Manager UI/API for any project OM manages, including multi-cluster workloads.

## Notes & caveats

- **At least one Backup Daemon is required.** On a MultiCluster OM with `backup.enabled: true`, the
  operator rejects the spec unless some `clusterSpecList` item has `backup.members > 0`
  ("At least one ClusterSpecList item must have backup members configured").
- **`clusterSpecList` ↔ topology.** A non-empty `clusterSpecList` requires `topology: MultiCluster`; a
  SingleCluster OM must not set it.
- **Oplog store is required for point-in-time restores.** With only a snapshot store you can restore
  whole snapshots but not to an arbitrary timestamp.
- **SCRAM-auth MongoDB stores need a user reference** — omitting `opLogStores[].mongodbUserRef` makes
  Backup go `Failed` until you add it.
- **Object Lock / immutable backups** (`s3Stores[].objectLockEnabled`) require an OM version that
  supports it.
- **Dedicated daemon cluster** — running Backup Daemons on a cluster with no OM app pods
  (`members: 0`, `backup.members: N`) is a supported and common layout.
- **Disabling Backup** (`enabled: false`) removes the daemons but does not delete data already written
  to external stores (S3 buckets, blockstore databases).

Relevant end-to-end coverage: `e2e_multi_cluster_backup_restore`,
`e2e_multi_cluster_backup_restore_no_mesh`, and `e2e_om_external_appdb_backup_and_restore`.
