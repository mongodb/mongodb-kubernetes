# Multi-cluster External AppDB — Fresh Start

This runbook deploys an Ops Manager whose Application Database is an **external, multi-cluster**
`MongoDBMultiCluster` resource (`role: AppDB`) referenced via `spec.externalApplicationDatabaseRef`
(`kind: MongoDBMultiCluster`), starting from scratch (the primary Ops Manager never has an
internally-managed AppDB).

See also: [02-mc-forward-migration](../02-mc-forward-migration) (move an existing internal
multi-cluster AppDB to an external one) and [03-mc-reverse-migration](../03-mc-reverse-migration)
(move back to an internal multi-cluster AppDB). For the single-cluster (`kind: MongoDB`) equivalents,
see [../../01-fresh-start](../../01-fresh-start).

> **Prerequisite — operator support.** Using a `MongoDBMultiCluster` as the external AppDB
> (`spec.externalApplicationDatabaseRef.kind: MongoDBMultiCluster`) requires an operator build that
> supports the multi-cluster external-AppDB feature (CLOUDP-444251). Older operators reject the
> `kind` value.

## Topology

```
 central cluster (K8S_CTX_0)                        member clusters (K8S_CTX_1, K8S_CTX_2)
 ───────────────────────────                        ──────────────────────────────────────
  operator (multi-cluster)                            AppDB data pods (role: AppDB)
  cert-manager                                        <appdb>-0-*   (K8S_CTX_1)
  management OM ──manages──► MongoDBMultiCluster ───► <appdb>-1-*   (K8S_CTX_2)
                             (role: AppDB)                  ▲
  primary OM ────────────────────────────────────────────-┘
   (externalApplicationDatabaseRef, kind: MongoDBMultiCluster; no spec.applicationDatabase)
```

- **Management Ops Manager** — an ordinary single-cluster Ops Manager (with its own internal AppDB) on
  the central cluster that *manages* the AppDB-role `MongoDBMultiCluster` resource.
- **External AppDB** — a `MongoDBMultiCluster` with `spec.role: AppDB`, its members spread across the
  member clusters via `clusterSpecList`. **Its name must be `<primary-om-name>-db`.** The members are
  TLS-enabled (cross-cluster traffic), so a CA ConfigMap + server certificate are provisioned.
- **Primary Ops Manager** — has **no** `spec.applicationDatabase`; it points at the external AppDB via
  `spec.externalApplicationDatabaseRef` (`kind: MongoDBMultiCluster`). Its
  `status.applicationDatabase.phase` is reported as `Disabled`.

## Prerequisites

- A **multi-cluster** Kubernetes environment: one central cluster and two member clusters, with the
  `kubectl mongodb` plugin available for `multicluster setup`.
- `kubectl` and `helm` configured with contexts for all three clusters.
- An operator build with multi-cluster external-AppDB support (see note above).

## Configure

Edit the placeholder values (the three contexts in particular), then source the environment:

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

1. `eadb-mc-01_0040_validate_env.sh` — validate required env vars and all three contexts.
2. `eadb-mc-01_0045_create_namespaces.sh` — create the namespace in every cluster.
3. `eadb-mc-01_0100_install_operator.sh` — `kubectl mongodb multicluster setup` + install the operator
   in multi-cluster mode.
4. `eadb-mc-01_0200_create_om_admin_secret.sh` — create the Ops Manager admin secret.
5. `eadb-mc-01_0205_install_cert_manager.sh` — install cert-manager (for AppDB TLS).
6. `eadb-mc-01_0206_configure_tls_prerequisites.sh` — self-signed CA chain + CA ConfigMap.
7. `eadb-mc-01_0207_generate_appdb_certificate.sh` — AppDB server certificate.
8. `eadb-mc-01_0210_deploy_management_om.sh` — deploy the management Ops Manager.
9. `eadb-mc-01_0215_wait_management_om.sh` — wait until `Running`; note its API-key secret.
10. `eadb-mc-01_0220_create_appdb_project_configmap.sh` — project ConfigMap (with `sslMMSCAConfigMap`).

### Fresh Start

11. `eadb-mc-01_0300_create_appdb_mongodbmulti.sh` — create the external AppDB `MongoDBMultiCluster`
    (`role: AppDB`, name `<primary-om-name>-db`, `clusterSpecList` across the member clusters).
12. `eadb-mc-01_0305_wait_appdb.sh` — wait until the AppDB is `Running`.
13. `eadb-mc-01_0310_create_primary_om.sh` — create the primary Ops Manager with
    `externalApplicationDatabaseRef` (`kind: MongoDBMultiCluster`) and no `applicationDatabase`.
14. `eadb-mc-01_0315_wait_primary_om.sh` — wait until the primary OM is `Running` and its AppDB status
    is `Disabled`.
15. `eadb-mc-01_0400_verify.sh` — verify per-member StatefulSet ownership (the `mongodbmulticluster`
    label) and the connection-string secret.

## Troubleshooting

- **`externalApplicationDatabaseRef.kind` rejected / unknown** — the operator predates the
  multi-cluster external-AppDB feature. Use a build that supports `kind: MongoDBMultiCluster`.
- **`externalApplicationDatabaseRef.name` rejected** — the referenced name must be exactly
  `<primary-om-name>-db`.
- **AppDB members never become ready / TLS errors** — check the CA ConfigMap (`${APPDB_CA_CONFIGMAP}`)
  has both `ca-pem` and `mms-ca.crt`, and that the AppDB certificate secret
  `${APPDB_CERT_PREFIX}-${APPDB_NAME}-cert` is `Ready`.
- **Primary OM AppDB phase is `Disabled`, not `Running`** — expected: in external-AppDB mode the
  operator does not manage the AppDB.
- **Ownership check** — multi-cluster AppDB StatefulSets are tracked by the `mongodbmulticluster`
  label per member cluster, not by `ownerReferences`.
