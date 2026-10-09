# Multi-cluster External AppDB — Forward Migration: step-by-step walkthrough

This guide migrates an Ops Manager from an **internally-managed multi-cluster AppDB**
(`spec.applicationDatabase` with `topology: MultiCluster`) to an **external** `MongoDBMultiCluster`
resource (`role: AppDB`) referenced through `spec.externalApplicationDatabaseRef`
(`kind: MongoDBMultiCluster`) — **without rolling the OM pods**. The AppDB StatefulSets keep the same
names and hosts, so the connection string does not change.

It shows only the migration-specific steps; for the shared setup (operator, cert-manager + CA,
management OM, project ConfigMap) it points at the fresh-start guide. Every step is a plain
`kubectl` command with the resource YAML inlined.

> This is the multi-cluster variant of [../../02-forward-migration](../../02-forward-migration). See
> also: [../01-mc-fresh-start](../01-mc-fresh-start) (start external from scratch) and
> [../03-mc-reverse-migration](../03-mc-reverse-migration) (move back to an internal AppDB).

> **Prerequisite — operator support.** Requires an operator build supporting
> `spec.externalApplicationDatabaseRef.kind: MongoDBMultiCluster`. The internal AppDB **must already
> be `topology: MultiCluster`**: the operator only allows switching to a `MongoDBMultiCluster`
> external AppDB when the current internal AppDB topology matches.

## How it works

```
BEFORE                                        AFTER
─────────                                     ─────────
primary OM ──owns──► StatefulSets             primary OM ──externalApplicationDatabaseRef──►┐
 (spec.applicationDatabase,                    (no spec.applicationDatabase)                │
  topology: MultiCluster)                                                                   ▼
                                              management OM ─manages─► MongoDBMultiCluster(role:AppDB)
                                                                              │ adopts
                                                                              ▼
                                                          per-member-cluster AppDB StatefulSets
```

1. The primary OM starts with an internal multi-cluster AppDB (`spec.applicationDatabase`,
   `topology: MultiCluster`, members spread across the member clusters, TLS-enabled). The operator
   owns one StatefulSet per member cluster, named `<primary-om-name>-db-<clusterIndex>`.
2. You create a `MongoDBMultiCluster` (`role: AppDB`) CR with **the same name, topology, members and
   TLS**, managed by the management OM. It cannot adopt the existing StatefulSets yet — the operator's
   **adoption gate** keeps it `Pending` while they are still owned by the OM.
3. You add `spec.externalApplicationDatabaseRef` to the primary OM. The operator **detaches** its
   internal AppDB StatefulSets per member cluster; the `MongoDBMultiCluster` CR then **adopts** them.
   Because the computed connection string is identical (same hosts), the OM pods do not roll.

## Prerequisites

This guide starts from an already-running multi-cluster deployment. Before you begin you must have:

- **Three** clusters (central + two AppDB members) with an Istio mesh, and the **multi-cluster
  operator installed** on the central cluster — see [../01-mc-fresh-start](../01-mc-fresh-start)
  steps 1–2.
- cert-manager, the **TLS CA ConfigMap** (`${APPDB_CA_CONFIGMAP}`, including the public roots for
  `downloads.mongodb.com`) and the AppDB server certificate — see
  [../01-mc-fresh-start](../01-mc-fresh-start) steps 4–6. **This CA setup is mandatory**; without the
  public roots the migrated AppDB agents fail the binary download (see that guide's Troubleshooting).
- A **management Ops Manager** that is `Running` and owns the external AppDB's project, plus its
  operator-provisioned API-key secret (`<namespace>-<management-om-name>-admin-key`) and the project
  ConfigMap (`${APPDB_PROJECT_CONFIGMAP}`) — see [../01-mc-fresh-start](../01-mc-fresh-start) steps
  7–9.
- A **primary Ops Manager** with an **internal multi-cluster AppDB** (the deployment you are
  migrating). If you do not have one yet, create it as in step 1 below.

Set the same variables as in [../01-mc-fresh-start](../01-mc-fresh-start) step 0 (`source
env_variables.sh`).

---

## 1. (Before state) Primary OM with an internal multi-cluster AppDB

If you are migrating an existing deployment, you already have this — skip to step 2. Otherwise, create
it. The internal AppDB **must be `topology: MultiCluster`** for the later switch to be allowed.

```bash
kubectl apply --context "${K8S_CTX_0}" -n "${MDB_NS}" -f - <<EOF
apiVersion: mongodb.com/v1
kind: MongoDBOpsManager
metadata:
  name: ${PRIMARY_OM_NAME}
spec:
  replicas: 1
  version: ${OM_VERSION}
  adminCredentials: ops-manager-admin-secret
  topology: MultiCluster
  clusterSpecList:
    - clusterName: ${K8S_CTX_0}
      members: 1
  applicationDatabase:
    topology: MultiCluster
    version: ${APPDB_VERSION}
    security:
      certsSecretPrefix: ${APPDB_CERT_PREFIX}
      tls:
        ca: ${APPDB_CA_CONFIGMAP}
    clusterSpecList:
      - clusterName: ${K8S_CTX_1}
        members: ${APPDB_MEMBERS_C1}
      - clusterName: ${K8S_CTX_2}
        members: ${APPDB_MEMBERS_C2}
  backup:
    enabled: false
  configuration:
    automation.versions.source: mongodb
    mms.ignoreInitialUiSetup: "true"
    mms.adminEmailAddr: cloud-manager-support@mongodb.com
    mms.fromEmailAddr: cloud-manager-support@mongodb.com
    mms.replyToEmailAddr: cloud-manager-support@mongodb.com
    mms.mail.hostname: email-smtp.us-east-1.amazonaws.com
    mms.mail.port: "465"
    mms.mail.ssl: "true"
    mms.mail.transport: smtp
    mms.minimumTLSVersion: TLSv1.2
EOF

# Wait for the internal AppDB and the OM to be Running.
kubectl wait --for=jsonpath='{.status.applicationDatabase.phase}'=Running \
  om/"${PRIMARY_OM_NAME}" --context "${K8S_CTX_0}" -n "${MDB_NS}" --timeout=1200s
kubectl wait --for=jsonpath='{.status.opsManager.phase}'=Running \
  om/"${PRIMARY_OM_NAME}" --context "${K8S_CTX_0}" -n "${MDB_NS}" --timeout=1800s
```

## 2. Create the external AppDB `MongoDBMultiCluster`

Same name (`<primary-om-name>-db`), topology, member layout and TLS as the internal AppDB. The primary
OM's internal AppDB still owns the StatefulSets, so this CR reports `Pending` on the adoption gate.

```bash
kubectl apply --context "${K8S_CTX_0}" -n "${MDB_NS}" -f - <<EOF
apiVersion: mongodb.com/v1
kind: MongoDBMultiCluster
metadata:
  name: ${APPDB_NAME}
spec:
  version: ${APPDB_VERSION}
  type: ReplicaSet
  role: AppDB
  persistent: true
  credentials: ${MANAGEMENT_OM_ADMIN_KEY_SECRET}
  opsManager:
    configMapRef:
      name: ${APPDB_PROJECT_CONFIGMAP}
  security:
    certsSecretPrefix: ${APPDB_CERT_PREFIX}
    tls:
      enabled: true
      ca: ${APPDB_CA_CONFIGMAP}
  clusterSpecList:
    - clusterName: ${K8S_CTX_1}
      members: ${APPDB_MEMBERS_C1}
    - clusterName: ${K8S_CTX_2}
      members: ${APPDB_MEMBERS_C2}
EOF
```

## 3. Confirm the AppDB CR is blocked on the adoption gate

It must **not** adopt the StatefulSets while they are still owned by the Ops Manager.

```bash
kubectl wait --for=jsonpath='{.status.phase}'=Pending \
  mdbmc/"${APPDB_NAME}" --context "${K8S_CTX_0}" -n "${MDB_NS}" --timeout=300s

kubectl get mdbmc "${APPDB_NAME}" --context "${K8S_CTX_0}" -n "${MDB_NS}" \
  -o jsonpath='{.status.message}{"\n"}'
```

## 4. Switch the primary OM to the external AppDB

Add `spec.externalApplicationDatabaseRef` (`kind: MongoDBMultiCluster`). The operator detaches its
internal AppDB StatefulSets per member cluster and marks them migration-ready, so the
`MongoDBMultiCluster` CR can adopt them. The connection string is unchanged, so the OM pods do not
roll.

```bash
kubectl patch om "${PRIMARY_OM_NAME}" \
  --context "${K8S_CTX_0}" -n "${MDB_NS}" \
  --type merge \
  -p "{\"spec\":{\"externalApplicationDatabaseRef\":{\"name\":\"${APPDB_NAME}\",\"kind\":\"MongoDBMultiCluster\"}}}"
```

## 5. Wait for adoption to complete

```bash
kubectl wait --for=jsonpath='{.status.phase}'=Running \
  mdbmc/"${APPDB_NAME}" --context "${K8S_CTX_0}" -n "${MDB_NS}" --timeout=1200s

kubectl wait --for=jsonpath='{.status.opsManager.phase}'=Running \
  om/"${PRIMARY_OM_NAME}" --context "${K8S_CTX_0}" -n "${MDB_NS}" --timeout=1800s
```

## 6. Verify

After the switch, each member cluster's AppDB StatefulSet must be owned by the external AppDB CR (the
`mongodbmulticluster` label equals `<namespace>-<appdb-name>`) and the migration annotation must be
cleared.

```bash
expected_owner="${MDB_NS}-${APPDB_NAME}"
idx=0
for ctx in "${K8S_CTX_1}" "${K8S_CTX_2}"; do
  sts="${APPDB_NAME}-${idx}"
  echo "--- cluster ${ctx} (StatefulSet ${sts}) ---"
  kubectl get statefulset "${sts}" --context "${ctx}" -n "${MDB_NS}" \
    -o jsonpath='  mongodbmulticluster label: {.metadata.labels.mongodbmulticluster}{"\n"}'
  kubectl get statefulset "${sts}" --context "${ctx}" -n "${MDB_NS}" \
    -o jsonpath='  appdb-migration-ready: {.metadata.annotations.mongodb\.com/appdb-migration-ready}{"\n"}'
  idx=$((idx + 1))
done
```

Expected: the `mongodbmulticluster` label equals `${MDB_NS}-${APPDB_NAME}` on both StatefulSets, and
the `appdb-migration-ready` annotation is empty.

## Troubleshooting

- **Switch rejected with a topology mismatch** — the internal AppDB must be `topology: MultiCluster`
  before switching to a `MongoDBMultiCluster` external AppDB. A single-cluster internal AppDB cannot
  migrate to a multi-cluster external one.
- **AppDB CR stays `Pending` ("Cannot take ownership of the AppDB StatefulSet")** — expected until the
  OM detaches; it clears once `externalApplicationDatabaseRef` is set (step 4).
- **AppDB agents fail the binary download with `x509: certificate signed by unknown authority`** — the
  TLS CA ConfigMap must include the public roots for `downloads.mongodb.com`, not only the self-signed
  CA. See [../01-mc-fresh-start](../01-mc-fresh-start) step 5 and its Troubleshooting.
- **Ownership check** — multi-cluster AppDB StatefulSets are tracked by the `mongodbmulticluster`
  label per member cluster, not by `ownerReferences`.
