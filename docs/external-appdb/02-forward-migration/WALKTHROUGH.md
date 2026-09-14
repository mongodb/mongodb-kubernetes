# External AppDB — Forward Migration: step-by-step walkthrough

This guide migrates an **existing** Ops Manager from an **internally-managed** Application Database
(`spec.applicationDatabase`) to an **external** one — a `MongoDB` resource (`role: AppDB`) referenced
through `spec.externalApplicationDatabaseRef` — **without downtime and without rolling the OM pods**.

It assumes the Ops Manager and its AppDB are already deployed (see Prerequisites) and only shows the
migration-specific steps. Every step is a plain `kubectl` command with the resource YAML inlined.

> See also: [../01-fresh-start](../01-fresh-start) (start external from scratch) and
> [../03-reverse-migration](../03-reverse-migration) (move back to an internal AppDB).

## How it works

```
BEFORE                                   AFTER
─────────                                ─────────
primary OM ──owns──► StatefulSet         primary OM ──externalApplicationDatabaseRef──►┐
 (spec.applicationDatabase)               (no spec.applicationDatabase)                │
                                                                                       ▼
                                         management OM ──manages──► MongoDB(role:AppDB) ──adopts──► StatefulSet
```

1. The primary OM starts with an internal AppDB; the operator owns a StatefulSet named
   `<primary-om-name>-db`.
2. You create a `MongoDB` (`role: AppDB`) CR with **the same name**, managed by the management OM. It
   cannot adopt the existing StatefulSet yet — the operator's **adoption gate** keeps it `Pending`
   ("Cannot take ownership of the AppDB StatefulSet") while the StatefulSet is still owned by the OM.
3. You add `spec.externalApplicationDatabaseRef` to the primary OM. The operator **detaches** its
   internal AppDB StatefulSet (drops its OwnerReference, marks it migration-ready); the `MongoDB` CR
   then **adopts** it. Because the computed connection string is identical (same hosts), the OM pods
   do not roll.

## Prerequisites

This guide starts from an already-running deployment. Before you begin you must have:

- A Kubernetes cluster with the **MongoDB Kubernetes operator installed** and `kubectl` configured.
- An existing **primary Ops Manager** (`${PRIMARY_OM_NAME}`, below) with an **internally-managed
  AppDB** — i.e. it has `spec.applicationDatabase` and the operator owns a StatefulSet named
  `<primary-om-name>-db`. This is the deployment you are migrating; **this guide does not cover
  deploying it.**
- A **management Ops Manager** (`${MANAGEMENT_OM_NAME}`, below) that is `Running` and will own the
  external AppDB's project. If you do not have one yet, stand it up first — see
  [../01-fresh-start](../01-fresh-start) steps 4–5 (deploy management OM) — this guide does not repeat
  those steps.
- The management OM's operator-provisioned programmatic API-key secret
  (`<namespace>-<management-om-name>-admin-key`) — created automatically once the management OM is
  `Running`.

Quick sanity check that the prerequisites are in place:

```bash
# primary OM Running with an internal AppDB, and its AppDB StatefulSet owned by the OM:
kubectl get om "${PRIMARY_OM_NAME}" --context "${K8S_CTX}" -n "${MDB_NS}" \
  -o jsonpath='{.status.opsManager.phase} / appdb={.status.applicationDatabase.phase}{"\n"}'
kubectl get statefulset "${APPDB_NAME}" --context "${K8S_CTX}" -n "${MDB_NS}" \
  -o jsonpath='{range .metadata.ownerReferences[*]}{.kind}/{.name}{"\n"}{end}'
# -> MongoDBOpsManager/${PRIMARY_OM_NAME}

# management OM Running and its API-key secret present:
kubectl get om "${MANAGEMENT_OM_NAME}" --context "${K8S_CTX}" -n "${MDB_NS}" \
  -o jsonpath='{.status.opsManager.phase}{"\n"}'
kubectl get secret "${MANAGEMENT_OM_ADMIN_KEY_SECRET}" --context "${K8S_CTX}" -n "${MDB_NS}"
```

## 0. Set your variables

```bash
export K8S_CTX="<your-kube-context>"     # kubectl config get-contexts
export MDB_NS="mongodb"                    # namespace of the existing OMs and the AppDB

# Names of the already-deployed Ops Managers:
export MANAGEMENT_OM_NAME="management-om"  # owns the external AppDB project
export PRIMARY_OM_NAME="primary-om"        # the OM being migrated

# The AppDB CR name is fixed by the operator's naming convention:
export APPDB_NAME="${PRIMARY_OM_NAME}-db"

# Must match the AppDB version already running under the primary OM's internal AppDB, and must be a
# MongoDB version the management OM offers (keep OM and AppDB on the same major line, e.g. 8.0.x).
export APPDB_VERSION="8.0.5-ent"

# In-cluster URL of the management OM and its operator-provisioned API-key secret:
export MANAGEMENT_OM_URL="http://${MANAGEMENT_OM_NAME}-svc.${MDB_NS}.svc.cluster.local:8080"
export MANAGEMENT_OM_ADMIN_KEY_SECRET="${MDB_NS}-${MANAGEMENT_OM_NAME}-admin-key"

# Project ConfigMap the AppDB CR will point at:
export APPDB_PROJECT_CONFIGMAP="${APPDB_NAME}-config"
export APPDB_PROJECT_NAME="external-appdb"
```

---

## 1. Create the AppDB project ConfigMap

Registers a project on the management OM for the external AppDB.

```bash
kubectl create configmap "${APPDB_PROJECT_CONFIGMAP}" \
  --context "${K8S_CTX}" -n "${MDB_NS}" \
  --from-literal=baseUrl="${MANAGEMENT_OM_URL}" \
  --from-literal=projectName="${APPDB_PROJECT_NAME}" \
  --from-literal=orgId=""
```

## 2. Create the external AppDB `MongoDB` (blocked on the adoption gate)

Same name as the OM's internal AppDB. It **cannot** adopt the StatefulSet yet, so it stays `Pending`.

```bash
kubectl apply --context "${K8S_CTX}" -n "${MDB_NS}" -f - <<EOF
apiVersion: mongodb.com/v1
kind: MongoDB
metadata:
  name: ${APPDB_NAME}
spec:
  members: 3
  version: ${APPDB_VERSION}
  type: ReplicaSet
  role: AppDB
  opsManager:
    configMapRef:
      name: ${APPDB_PROJECT_CONFIGMAP}
  credentials: ${MANAGEMENT_OM_ADMIN_KEY_SECRET}
  persistent: true
  security:
    authentication:
      enabled: true
      modes: ["SCRAM"]
      ignoreUnknownUsers: true
EOF
```

Confirm it is held on the adoption gate (this is expected — not an error):

```bash
kubectl wait --for=jsonpath='{.status.phase}'=Pending \
  mdb/"${APPDB_NAME}" --context "${K8S_CTX}" -n "${MDB_NS}" --timeout=300s

kubectl get mdb "${APPDB_NAME}" --context "${K8S_CTX}" -n "${MDB_NS}" \
  -o jsonpath='{.status.message}{"\n"}'
# -> "Cannot take ownership of the AppDB StatefulSet: ..."
```

## 3. Flip the primary OM to the external AppDB

Add `spec.externalApplicationDatabaseRef`. The operator detaches its internal AppDB StatefulSet so
the `MongoDB` CR can adopt it. The connection string is unchanged, so the OM pods do not roll.

```bash
kubectl patch om "${PRIMARY_OM_NAME}" \
  --context "${K8S_CTX}" -n "${MDB_NS}" \
  --type merge \
  -p "{\"spec\":{\"externalApplicationDatabaseRef\":{\"name\":\"${APPDB_NAME}\",\"kind\":\"MongoDB\"}}}"
```

> If your OM also has an explicit `spec.applicationDatabase` block you want removed, you can drop it
> in the same patch; leaving it is harmless — once `externalApplicationDatabaseRef` is set the
> operator stops managing an internal AppDB for this OM.

## 4. Wait for the switch to complete

```bash
kubectl wait --for=jsonpath='{.status.phase}'=Running \
  mdb/"${APPDB_NAME}" --context "${K8S_CTX}" -n "${MDB_NS}" --timeout=1200s

kubectl wait --for=jsonpath='{.status.opsManager.phase}'=Running \
  om/"${PRIMARY_OM_NAME}" --context "${K8S_CTX}" -n "${MDB_NS}" --timeout=1800s
```

## 5. Verify

```bash
kubectl get pods --context "${K8S_CTX}" -n "${MDB_NS}"

# The AppDB StatefulSet must now be owned solely by the MongoDB CR (not the OM):
kubectl get statefulset "${APPDB_NAME}" --context "${K8S_CTX}" -n "${MDB_NS}" \
  -o jsonpath='{range .metadata.ownerReferences[*]}{.kind}/{.name}{"\n"}{end}'
# -> MongoDB/${APPDB_NAME}

# The migration-ready annotation should be cleared after adoption:
kubectl get statefulset "${APPDB_NAME}" --context "${K8S_CTX}" -n "${MDB_NS}" \
  -o jsonpath='{.metadata.annotations.mongodb\.com/appdb-migration-ready}{"\n"}'
```

You should see the AppDB StatefulSet owned by `MongoDB/${APPDB_NAME}`, the migration annotation
empty, and the primary OM `Running` against the external AppDB — with the OM pods never having
rolled during the switch.

## Troubleshooting

- **AppDB `MongoDB` is `Failed` with `Invalid config: MongoDB version <x> is not available`** — the
  external AppDB is an Ops-Manager-managed MongoDB; its `spec.version` must be a version the
  **management OM** offers. Keep the management OM and `APPDB_VERSION` on the same major line (e.g. OM
  `8.0.x` + AppDB `8.0.x-ent`).
- **AppDB CR stays `Pending` after the switch** — the operator must first detach the StatefulSet.
  Confirm `spec.externalApplicationDatabaseRef` was applied to the primary OM and its name equals
  `<primary-om-name>-db`.
- **AppDB CR `Pending` with a project/registration error** — the management OM's public REST API may
  not have been ready when the CR was created. Confirm the management OM is `Running` and its API
  answers (`GET /api/public/v1.0` returns `401`), then re-reconcile.
- **OM pods rolled during the switch** — indicates the computed connection string changed. For the
  default-port replica-set case it is identical; non-default ports are out of scope.
</content>
