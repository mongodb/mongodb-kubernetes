# External AppDB — Fresh Start: step-by-step walkthrough

This guide shows how to deploy an Ops Manager whose Application Database is an **external**
`MongoDB` resource (`role: AppDB`) referenced through `spec.externalApplicationDatabaseRef`,
starting from scratch. Every step here is a plain `kubectl`/`helm` command with the resource YAML
inlined, so you can reproduce it on your own cluster without any test harness.

> For the migration variants see [../02-forward-migration](../02-forward-migration) (move an existing
> internal AppDB to an external one) and [../03-reverse-migration](../03-reverse-migration) (move
> back to an internal AppDB).

## Topology

```
                 manages (project + credentials)
 management OM ───────────────────────────────► MongoDB (role: AppDB)   name = "<primary-om-name>-db"
                                                        ▲
                                                        │ externalApplicationDatabaseRef
                                                        │
                                                   primary OM  (no spec.applicationDatabase)
```

- **Management Ops Manager** — an ordinary Ops Manager (with its own internal AppDB) that *manages*
  the AppDB-role `MongoDB` resource the same way it manages any other database project.
- **External AppDB** — a `MongoDB` replica set with `spec.role: AppDB`. **Its name must be exactly
  `<primary-om-name>-db`**; the operator enforces this when resolving `externalApplicationDatabaseRef`.
- **Primary Ops Manager** — has **no** `spec.applicationDatabase`; it references the external AppDB
  via `spec.externalApplicationDatabaseRef`. The operator resolves that `MongoDB` CR to compute the
  AppDB connection string (and its TLS/CA if enabled) and never creates an internal AppDB
  StatefulSet for this OM. Its `status.applicationDatabase.phase` is therefore reported as
  `Disabled` (not `Running`).

## Prerequisites

- A running Kubernetes cluster with a default StorageClass.
- `kubectl` and `helm` configured for that cluster.
- Access to the MongoDB Kubernetes operator Helm chart.

## 0. Set your variables

```bash
export K8S_CTX="<your-kube-context>"     # kubectl config get-contexts
export MDB_NS="mongodb"                    # namespace for operator, both OMs and the AppDB

export OPERATOR_HELM_CHART="oci://quay.io/mongodb/helm-charts/mongodb-kubernetes"

# IMPORTANT: OM_VERSION and APPDB_VERSION must be a consistent pair. The external AppDB is an
# Ops-Manager-managed MongoDB, so APPDB_VERSION must be a MongoDB version the management OM actually
# offers. An 8.0.x Ops Manager offers 8.0.x MongoDB; a 7.0.x OM does NOT — see Troubleshooting.
export OM_VERSION="8.0.7"                  # management + primary Ops Manager version
export APPDB_VERSION="8.0.5-ent"           # MongoDB version for the AppDB

export MANAGEMENT_OM_NAME="management-om"
export PRIMARY_OM_NAME="primary-om"

# The AppDB CR name is fixed by the operator's naming convention:
export APPDB_NAME="${PRIMARY_OM_NAME}-db"

# In-cluster URL of the management Ops Manager:
export MANAGEMENT_OM_URL="http://${MANAGEMENT_OM_NAME}-svc.${MDB_NS}.svc.cluster.local:8080"

# Programmatic API-key secret the operator provisions for the management OM
# (naming format: "<namespace>-<om-name>-admin-key"):
export MANAGEMENT_OM_ADMIN_KEY_SECRET="${MDB_NS}-${MANAGEMENT_OM_NAME}-admin-key"

# Project connection ConfigMap the AppDB CR points at:
export APPDB_PROJECT_CONFIGMAP="${APPDB_NAME}-config"
export APPDB_PROJECT_NAME="external-appdb"

# Connection-string secret the operator computes for the primary OM:
export APPDB_CONNECTION_STRING_SECRET="${APPDB_NAME}-connection-string"

# Ops Manager admin user (first global admin of each OM):
export OM_ADMIN_EMAIL="admin@example.com"
export OM_ADMIN_PASSWORD="Passw0rd."
export OM_ADMIN_FIRST_NAME="Admin"
export OM_ADMIN_LAST_NAME="User"
```

---

## 1. Create the namespace

```bash
kubectl create namespace "${MDB_NS}" --context "${K8S_CTX}"
```

## 2. Install the operator

```bash
helm upgrade --install --kube-context "${K8S_CTX}" \
  --namespace "${MDB_NS}" --create-namespace \
  mongodb-kubernetes "${OPERATOR_HELM_CHART}"

kubectl rollout status deployment/mongodb-kubernetes-operator \
  --context "${K8S_CTX}" -n "${MDB_NS}" --timeout=300s
```

## 3. Create the Ops Manager admin secret

Both Ops Managers reference this secret via `spec.adminCredentials`.

```bash
kubectl create secret generic ops-manager-admin-secret \
  --context "${K8S_CTX}" -n "${MDB_NS}" \
  --from-literal=Username="${OM_ADMIN_EMAIL}" \
  --from-literal=Password="${OM_ADMIN_PASSWORD}" \
  --from-literal=FirstName="${OM_ADMIN_FIRST_NAME}" \
  --from-literal=LastName="${OM_ADMIN_LAST_NAME}"
```

## 4. Deploy the management Ops Manager

An ordinary Ops Manager with its own internal AppDB. It will own the project under which the
external AppDB `MongoDB` resource is registered.

> The `mms.*` mail settings are **required**: with `mms.ignoreInitialUiSetup: "true"` the OM
> pre-flight check refuses to start unless `mms.fromEmailAddr` (and the related mail settings) are
> present. See Troubleshooting.

```bash
kubectl apply --context "${K8S_CTX}" -n "${MDB_NS}" -f - <<EOF
apiVersion: mongodb.com/v1
kind: MongoDBOpsManager
metadata:
  name: ${MANAGEMENT_OM_NAME}
spec:
  replicas: 1
  version: ${OM_VERSION}
  adminCredentials: ops-manager-admin-secret
  applicationDatabase:
    members: 3
    version: ${APPDB_VERSION}
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
```

## 5. Wait for the management Ops Manager

Wait for the internal AppDB, then the OM application, then confirm the public REST API is actually
serving (see the note below).

```bash
kubectl wait --for=jsonpath='{.status.applicationDatabase.phase}'=Running \
  om/"${MANAGEMENT_OM_NAME}" --context "${K8S_CTX}" -n "${MDB_NS}" --timeout=1200s

kubectl wait --for=jsonpath='{.status.opsManager.phase}'=Running \
  om/"${MANAGEMENT_OM_NAME}" --context "${K8S_CTX}" -n "${MDB_NS}" --timeout=1800s
```

`opsManager.phase=Running` only means the OM pod is up; the public REST API the operator needs to
create the AppDB project can lag a few seconds. Confirm it is answering before continuing (a `401`
is the expected unauthenticated response and means the API layer is ready):

```bash
om_pod="${MANAGEMENT_OM_NAME}-0"
until [ "$(kubectl exec "${om_pod}" -c mongodb-ops-manager --context "${K8S_CTX}" -n "${MDB_NS}" -- \
  curl -s -o /dev/null -w '%{http_code}' \
  "http://$(kubectl get pod "${om_pod}" --context "${K8S_CTX}" -n "${MDB_NS}" \
    -o jsonpath='{.status.podIP}'):8080/api/public/v1.0" 2>/dev/null)" = "401" ]; do
  echo "waiting for management OM public API..."; sleep 10
done
echo "management OM public API is serving"
```

The operator provisions a programmatic API-key secret for the management OM; the AppDB CR will use
it as its credentials:

```bash
kubectl get secret "${MANAGEMENT_OM_ADMIN_KEY_SECRET}" --context "${K8S_CTX}" -n "${MDB_NS}"
```

## 6. Create the AppDB project ConfigMap

Points the AppDB CR at a project on the management OM.

```bash
kubectl create configmap "${APPDB_PROJECT_CONFIGMAP}" \
  --context "${K8S_CTX}" -n "${MDB_NS}" \
  --from-literal=baseUrl="${MANAGEMENT_OM_URL}" \
  --from-literal=projectName="${APPDB_PROJECT_NAME}" \
  --from-literal=orgId=""
```

## 7. Create the external AppDB `MongoDB`

A normal replica set with `role: AppDB`. **The name must be `${APPDB_NAME}` (`<primary-om-name>-db`).**

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

kubectl wait --for=jsonpath='{.status.phase}'=Running \
  mdb/"${APPDB_NAME}" --context "${K8S_CTX}" -n "${MDB_NS}" --timeout=1200s
```

## 8. Create the primary Ops Manager

No `spec.applicationDatabase` — it references the external AppDB instead.

```bash
kubectl apply --context "${K8S_CTX}" -n "${MDB_NS}" -f - <<EOF
apiVersion: mongodb.com/v1
kind: MongoDBOpsManager
metadata:
  name: ${PRIMARY_OM_NAME}
spec:
  replicas: 1
  version: ${OM_VERSION}
  adminCredentials: ops-manager-admin-secret
  externalApplicationDatabaseRef:
    name: ${APPDB_NAME}
    kind: MongoDB
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
```

## 9. Wait for the primary Ops Manager

```bash
kubectl wait --for=jsonpath='{.status.opsManager.phase}'=Running \
  om/"${PRIMARY_OM_NAME}" --context "${K8S_CTX}" -n "${MDB_NS}" --timeout=1800s

# In external-AppDB mode the operator does not manage an internal AppDB for this OM,
# so its AppDB status is Disabled (this is expected, not an error):
kubectl wait --for=jsonpath='{.status.applicationDatabase.phase}'=Disabled \
  om/"${PRIMARY_OM_NAME}" --context "${K8S_CTX}" -n "${MDB_NS}" --timeout=600s
```

## 10. Verify

```bash
kubectl get pods --context "${K8S_CTX}" -n "${MDB_NS}"

# The AppDB StatefulSet must be owned solely by the MongoDB CR (not by the primary OM):
kubectl get statefulset "${APPDB_NAME}" --context "${K8S_CTX}" -n "${MDB_NS}" \
  -o jsonpath='{range .metadata.ownerReferences[*]}{.kind}/{.name}{"\n"}{end}'

# The operator computes the AppDB connection string for the primary OM:
kubectl get secret "${APPDB_CONNECTION_STRING_SECRET}" --context "${K8S_CTX}" -n "${MDB_NS}"
```

You should see: the management OM, the external AppDB (`${APPDB_NAME}-0/1/2`) and the primary OM
pods all `Running`; the AppDB StatefulSet owned by `MongoDB/${APPDB_NAME}`; and the connection-string
secret present.

## Troubleshooting

- **AppDB `MongoDB` is `Failed` with `Invalid config: MongoDB version <x> is not available`** — the
  external AppDB is an Ops-Manager-managed MongoDB, so its `spec.version` must be a version the
  **management OM** offers in its version manifest. Keep `OM_VERSION` and `APPDB_VERSION` on the same
  major line (e.g. OM `8.0.x` with AppDB `8.0.x-ent`). A `7.0.x` OM does not offer `8.0.x` MongoDB.
  (Note the management OM's *own* internal AppDB is unaffected because it downloads binaries directly
  via `automation.versions.source: mongodb`; only the external AppDB is gated by the OM manifest.)
- **Management OM pod CrashLoops right after start; logs show
  `mms.ignoreInitialUiSetup ... mms.fromEmailAddr must not be blank`** — add the `mms.*` mail block
  shown in step 4 to `spec.configuration`.
- **AppDB `MongoDB` stays `Pending` and its agents never get an automation config (empty
  `agent-health-status.json`)** — the AppDB CR was created before the management OM's public REST API
  was serving, so the operator could not create/seed the project. Ensure the readiness poll in step 5
  passes before step 7.
- **`externalApplicationDatabaseRef.name` rejected** — the referenced `MongoDB` name must be exactly
  `<primary-om-name>-db`.
- **Primary OM `status.applicationDatabase.phase` is `Disabled`, not `Running`** — expected: in
  external-AppDB mode the operator does not manage the AppDB for this OM.
