# Multi-cluster External AppDB — Fresh Start: step-by-step walkthrough

This guide shows how to deploy an Ops Manager whose Application Database is an **external,
multi-cluster** `MongoDBMultiCluster` resource (`role: AppDB`) referenced through
`spec.externalApplicationDatabaseRef`, starting from scratch. The AppDB data pods are spread across
two member clusters; the operator and both Ops Managers run on a central cluster. Every step is a
plain `kubectl`/`helm` command with the resource YAML inlined, so you can reproduce it on your own
clusters without any test harness.

> This is the multi-cluster variant of [../../01-fresh-start](../../01-fresh-start). For the migration
> variants see [../02-mc-forward-migration](../02-mc-forward-migration) (move an existing internal
> AppDB to an external multi-cluster one) and [../03-mc-reverse-migration](../03-mc-reverse-migration)
> (move back to an internal AppDB).

> **Prerequisite:** the external multi-cluster AppDB feature
> (`externalApplicationDatabaseRef.kind: MongoDBMultiCluster`) requires an operator build that
> supports it. It does not work on older operators.

## Topology

```
 central cluster (K8S_CTX_0)                 member clusters (K8S_CTX_1, K8S_CTX_2)
 ┌───────────────────────────┐              ┌─────────────────────────────────────┐
 │ operator (multi-cluster)  │   manages    │  MongoDBMultiCluster (role: AppDB)   │
 │ management OM ────────────┼─────────────►│  name = "<primary-om-name>-db"       │
 │ (internal AppDB)          │ project+creds│  members spread across both clusters │
 │                           │              └─────────────────────────────────────┘
 │ primary OM                │                      ▲
 │ (no applicationDatabase) ─┼──────────────────────┘ externalApplicationDatabaseRef
 │ MongoDBMultiCluster CR    │                          (kind: MongoDBMultiCluster)
 └───────────────────────────┘
```

- **Central cluster (`K8S_CTX_0`)** — runs the operator (in multi-cluster mode), cert-manager, both
  Ops Managers, and holds the `MongoDBMultiCluster` CR object. The operator fans the AppDB members
  out to the member clusters.
- **Member clusters (`K8S_CTX_1`, `K8S_CTX_2`)** — host the AppDB data pods. One AppDB StatefulSet
  per cluster, named `<appdb>-<clusterIndex>`.
- **Management Ops Manager** — an ordinary single-cluster Ops Manager (with its own internal AppDB)
  that *manages* the AppDB-role resource the same way it manages any other database project.
- **External AppDB** — a `MongoDBMultiCluster` with `spec.role: AppDB`. **Its name must be exactly
  `<primary-om-name>-db`**; the operator enforces this when resolving
  `externalApplicationDatabaseRef`. Because members talk to each other across clusters, **TLS is
  enabled** and the pods run inside an Istio service mesh.
- **Primary Ops Manager** — has **no** `spec.applicationDatabase`; it references the external AppDB
  via `spec.externalApplicationDatabaseRef` (`kind: MongoDBMultiCluster`). Its
  `status.applicationDatabase.phase` is reported as `Disabled` (not `Running`), because the operator
  does not manage an internal AppDB for this OM.

## Prerequisites

- **Three** Kubernetes clusters (one central + two AppDB members), each with a default StorageClass.
- An **Istio service mesh** spanning the clusters, with cross-cluster service discovery. The AppDB
  pods need a sidecar to reach the central-cluster management OM and each other over the mesh.
- `kubectl`, `helm`, and the `kubectl-mongodb` plugin configured for all three clusters.
- [cert-manager](https://cert-manager.io/) installed on the central cluster (step 5).
- Access to the MongoDB Kubernetes operator Helm chart.

## 0. Set your variables

```bash
# Three kube contexts (kubectl config get-contexts):
export K8S_CTX_0="<central cluster context>"      # operator, cert-manager, both OMs, the CR object
export K8S_CTX_1="<appdb member cluster 1 context>"
export K8S_CTX_2="<appdb member cluster 2 context>"

export MDB_NS="mongodb"                             # namespace used in every cluster

export OPERATOR_HELM_CHART="oci://quay.io/mongodb/helm-charts/mongodb-kubernetes"

# IMPORTANT: OM_VERSION and APPDB_VERSION must be a consistent pair. The external AppDB is an
# Ops-Manager-managed MongoDB, so APPDB_VERSION must be a MongoDB version the management OM actually
# offers. An 8.0.x Ops Manager offers 8.0.x MongoDB; a 7.0.x OM does NOT — see Troubleshooting.
export OM_VERSION="8.0.7"                           # management + primary Ops Manager version
export APPDB_VERSION="8.0.5-ent"                    # MongoDB version for the AppDB

export MANAGEMENT_OM_NAME="management-om"
export PRIMARY_OM_NAME="primary-om"

# The AppDB CR name is fixed by the operator's naming convention:
export APPDB_NAME="${PRIMARY_OM_NAME}-db"

# AppDB members per member cluster (clusterSpecList):
export APPDB_MEMBERS_C1=2
export APPDB_MEMBERS_C2=2

# In-cluster URL of the management Ops Manager (on the central cluster):
export MANAGEMENT_OM_URL="http://${MANAGEMENT_OM_NAME}-svc.${MDB_NS}.svc.cluster.local:8080"

# Programmatic API-key secret the operator provisions for the management OM
# (naming format: "<namespace>-<om-name>-admin-key"):
export MANAGEMENT_OM_ADMIN_KEY_SECRET="${MDB_NS}-${MANAGEMENT_OM_NAME}-admin-key"

# Project connection ConfigMap the AppDB CR points at:
export APPDB_PROJECT_CONFIGMAP="${APPDB_NAME}-config"
export APPDB_PROJECT_NAME="external-appdb"

# Connection-string secret the operator computes for the primary OM:
export APPDB_CONNECTION_STRING_SECRET="${APPDB_NAME}-connection-string"

# TLS material (cert-manager issuers, CA, AppDB server-cert prefix):
export CERT_MANAGER_NAMESPACE="cert-manager"
export APPDB_TLS_SELF_SIGNED_ISSUER="mongodb-self-signed-issuer"
export APPDB_TLS_CA_CERT_NAME="mongodb-ca"
export APPDB_TLS_CA_SECRET_NAME="mongodb-ca-key-pair"
export APPDB_TLS_CA_ISSUER="mongodb-ca-issuer"
export APPDB_CERT_PREFIX="appdb"
# CA ConfigMap (keys ca-pem + mms-ca.crt) the AppDB and OM trust for TLS:
export APPDB_CA_CONFIGMAP="${APPDB_NAME}-ca"

# Ops Manager admin user (first global admin of each OM):
export OM_ADMIN_EMAIL="admin@example.com"
export OM_ADMIN_PASSWORD="Passw0rd."
export OM_ADMIN_FIRST_NAME="Admin"
export OM_ADMIN_LAST_NAME="User"
```

---

## 1. Create the namespace in every cluster

The namespace must exist on the central cluster **and** both member clusters. The
`istio-injection=enabled` label gives the AppDB pods a sidecar so their agents can resolve the
central-cluster management OM service across clusters — without it they fail with "Could not resolve
host" when downloading the automation agent from the OM `baseUrl`.

```bash
for ctx in "${K8S_CTX_0}" "${K8S_CTX_1}" "${K8S_CTX_2}"; do
  kubectl create namespace "${MDB_NS}" --context "${ctx}" --dry-run=client -o yaml \
    | kubectl apply --context "${ctx}" -f -
  kubectl label namespace "${MDB_NS}" istio-injection=enabled --overwrite --context "${ctx}"
done
```

## 2. Install the operator in multi-cluster mode

First wire up service accounts and roles across all clusters with the `kubectl-mongodb` plugin, then
install the operator on the central cluster with the member clusters registered.

```bash
kubectl mongodb multicluster setup \
  --central-cluster="${K8S_CTX_0}" \
  --member-clusters="${K8S_CTX_0},${K8S_CTX_1},${K8S_CTX_2}" \
  --member-cluster-namespace="${MDB_NS}" \
  --central-cluster-namespace="${MDB_NS}" \
  --create-service-account-secrets \
  --install-database-roles=true

helm upgrade --install --kube-context "${K8S_CTX_0}" \
  --create-namespace --namespace="${MDB_NS}" \
  mongodb-kubernetes-operator-multi-cluster \
  --set operator.name=mongodb-kubernetes-operator-multi-cluster \
  --set operator.createOperatorServiceAccount=false \
  --set operator.createResourcesServiceAccountsAndRoles=false \
  --set "multiCluster.clusters={${K8S_CTX_0},${K8S_CTX_1},${K8S_CTX_2}}" \
  "${OPERATOR_HELM_CHART}"

kubectl --context "${K8S_CTX_0}" -n "${MDB_NS}" rollout status \
  --timeout=300s deployment/mongodb-kubernetes-operator-multi-cluster
```

## 3. Create the Ops Manager admin secret

Both Ops Managers reference this secret via `spec.adminCredentials`.

```bash
kubectl create secret generic ops-manager-admin-secret \
  --context "${K8S_CTX_0}" -n "${MDB_NS}" \
  --from-literal=Username="${OM_ADMIN_EMAIL}" \
  --from-literal=Password="${OM_ADMIN_PASSWORD}" \
  --from-literal=FirstName="${OM_ADMIN_FIRST_NAME}" \
  --from-literal=LastName="${OM_ADMIN_LAST_NAME}"
```

## 4. Install cert-manager (central cluster)

The multi-cluster AppDB members are TLS-enabled, so you need cert-manager on the central cluster to
issue the CA and the AppDB server certificate.

```bash
helm upgrade --install --kube-context "${K8S_CTX_0}" \
  cert-manager jetstack/cert-manager \
  --namespace "${CERT_MANAGER_NAMESPACE}" --create-namespace \
  --set crds.enabled=true
```

## 5. Configure the TLS CA

Create the signing chain (self-signed `ClusterIssuer` → CA `Certificate` → CA `ClusterIssuer`), then
publish the CA into a ConfigMap the operator references as `spec.security.tls.ca` (keys `ca-pem` and
`mms-ca.crt`).

> **Important (multi-cluster specific):** when AppDB TLS is enabled, the automation agent validates
> **every** outbound HTTPS connection — including downloading the MongoDB binary from
> `downloads.mongodb.com` — against this CA bundle, *not* the system trust store. So the bundle must
> trust **both** the self-signed CA (for AppDB member TLS) **and** the public roots that sign
> `downloads.mongodb.com`. We append the host's system CA bundle to cover the public roots. (A
> self-signed-only bundle makes the agent fail the binary download with
> `x509: certificate signed by unknown authority` — see Troubleshooting.) The ConfigMap is applied
> server-side because the combined bundle exceeds the 262144-byte limit of the client-side apply
> annotation.

```bash
# Self-signed ClusterIssuer bootstraps a CA.
kubectl apply --context "${K8S_CTX_0}" -f - <<EOF
apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata:
  name: ${APPDB_TLS_SELF_SIGNED_ISSUER}
spec:
  selfSigned: {}
EOF

# CA Certificate.
kubectl apply --context "${K8S_CTX_0}" -n "${CERT_MANAGER_NAMESPACE}" -f - <<EOF
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: ${APPDB_TLS_CA_CERT_NAME}
spec:
  isCA: true
  commonName: mongodb-ca
  secretName: ${APPDB_TLS_CA_SECRET_NAME}
  duration: 87600h
  renewBefore: 8760h
  privateKey:
    algorithm: ECDSA
    size: 256
  issuerRef:
    name: ${APPDB_TLS_SELF_SIGNED_ISSUER}
    kind: ClusterIssuer
EOF

kubectl wait --for=condition=Ready certificate/"${APPDB_TLS_CA_CERT_NAME}" \
  -n "${CERT_MANAGER_NAMESPACE}" --context "${K8S_CTX_0}" --timeout=120s

# CA ClusterIssuer backed by that CA.
kubectl apply --context "${K8S_CTX_0}" -f - <<EOF
apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata:
  name: ${APPDB_TLS_CA_ISSUER}
spec:
  ca:
    secretName: ${APPDB_TLS_CA_SECRET_NAME}
EOF

# Publish the CA ConfigMap, combining the self-signed CA with the host's public CA bundle.
ca_crt=$(kubectl get secret "${APPDB_TLS_CA_SECRET_NAME}" \
  -n "${CERT_MANAGER_NAMESPACE}" --context "${K8S_CTX_0}" \
  -o jsonpath='{.data.ca\.crt}' | base64 -d)

system_ca=""
for candidate in /etc/ssl/certs/ca-certificates.crt /etc/pki/tls/certs/ca-bundle.crt /etc/ssl/cert.pem; do
  [[ -r "${candidate}" ]] && { system_ca=$(cat "${candidate}"); break; }
done

ca_bundle_file=$(mktemp)
printf '%s\n%s\n' "${ca_crt}" "${system_ca}" > "${ca_bundle_file}"

kubectl create configmap "${APPDB_CA_CONFIGMAP}" \
  --context "${K8S_CTX_0}" -n "${MDB_NS}" \
  --from-file=ca-pem="${ca_bundle_file}" \
  --from-file=mms-ca.crt="${ca_bundle_file}" \
  --dry-run=client -o yaml \
  | kubectl apply --server-side --force-conflicts --context "${K8S_CTX_0}" -n "${MDB_NS}" -f -

rm -f "${ca_bundle_file}"
```

## 6. Generate the AppDB server certificate

The cert secret name follows the operator convention `<certsSecretPrefix>-<appdb-name>-cert`. The
wildcard `dnsName` covers every per-member AppDB Service FQDN
(`<appdb>-<clusterIndex>-<memberIndex>-svc.<ns>.svc.cluster.local`).

```bash
kubectl apply --context "${K8S_CTX_0}" -n "${MDB_NS}" -f - <<EOF
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: ${APPDB_CERT_PREFIX}-${APPDB_NAME}-cert
spec:
  secretName: ${APPDB_CERT_PREFIX}-${APPDB_NAME}-cert
  duration: 8760h
  renewBefore: 720h
  privateKey:
    algorithm: RSA
    size: 2048
  usages: ["server auth", "client auth"]
  dnsNames:
    - "*.${MDB_NS}.svc.cluster.local"
  issuerRef:
    name: ${APPDB_TLS_CA_ISSUER}
    kind: ClusterIssuer
EOF

kubectl wait --for=condition=Ready certificate/"${APPDB_CERT_PREFIX}-${APPDB_NAME}-cert" \
  -n "${MDB_NS}" --context "${K8S_CTX_0}" --timeout=120s
```

## 7. Deploy the management Ops Manager

An ordinary single-cluster Ops Manager with its own internal AppDB, on the central cluster. It owns
the project under which the external AppDB is registered.

> The `mms.*` mail settings are **required**: with `mms.ignoreInitialUiSetup: "true"` the OM
> pre-flight check refuses to start unless `mms.fromEmailAddr` (and the related mail settings) are
> present. See Troubleshooting.

```bash
kubectl apply --context "${K8S_CTX_0}" -n "${MDB_NS}" -f - <<EOF
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

## 8. Wait for the management Ops Manager

Wait for the internal AppDB, then the OM application, then confirm the public REST API is serving
(the operator needs it to create the AppDB project).

```bash
kubectl wait --for=jsonpath='{.status.applicationDatabase.phase}'=Running \
  om/"${MANAGEMENT_OM_NAME}" --context "${K8S_CTX_0}" -n "${MDB_NS}" --timeout=1200s

kubectl wait --for=jsonpath='{.status.opsManager.phase}'=Running \
  om/"${MANAGEMENT_OM_NAME}" --context "${K8S_CTX_0}" -n "${MDB_NS}" --timeout=1800s

# opsManager.phase=Running only means the pod is up; the public REST API can lag a few seconds.
# A 401 is the expected unauthenticated response and means the API layer is ready.
om_pod="${MANAGEMENT_OM_NAME}-0"
until [ "$(kubectl exec "${om_pod}" -c mongodb-ops-manager --context "${K8S_CTX_0}" -n "${MDB_NS}" -- \
  curl -s -o /dev/null -w '%{http_code}' \
  "http://$(kubectl get pod "${om_pod}" --context "${K8S_CTX_0}" -n "${MDB_NS}" \
    -o jsonpath='{.status.podIP}'):8080/api/public/v1.0" 2>/dev/null)" = "401" ]; do
  echo "waiting for management OM public API..."; sleep 10
done
```

The operator provisions a programmatic API-key secret for the management OM; the AppDB CR uses it as
its credentials:

```bash
kubectl get secret "${MANAGEMENT_OM_ADMIN_KEY_SECRET}" --context "${K8S_CTX_0}" -n "${MDB_NS}"
```

## 9. Create the AppDB project ConfigMap

Points the AppDB CR at a project on the management OM. `sslMMSCAConfigMap` advertises the CA the
TLS-enabled AppDB agents must trust when reaching Ops Manager.

```bash
kubectl create configmap "${APPDB_PROJECT_CONFIGMAP}" \
  --context "${K8S_CTX_0}" -n "${MDB_NS}" \
  --from-literal=baseUrl="${MANAGEMENT_OM_URL}" \
  --from-literal=projectName="${APPDB_PROJECT_NAME}" \
  --from-literal=sslMMSCAConfigMap="${APPDB_CA_CONFIGMAP}" \
  --from-literal=orgId="" \
  --dry-run=client -o yaml \
  | kubectl apply --context "${K8S_CTX_0}" -n "${MDB_NS}" -f -
```

## 10. Create the external AppDB `MongoDBMultiCluster`

A `MongoDBMultiCluster` with `role: AppDB`, spread across the two member clusters, with TLS enabled.
**The name must be `${APPDB_NAME}` (`<primary-om-name>-db`).** The CR object is applied on the
central cluster; the operator fans the members out to the member clusters.

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

kubectl wait --for=jsonpath='{.status.phase}'=Running \
  mdbmc/"${APPDB_NAME}" --context "${K8S_CTX_0}" -n "${MDB_NS}" --timeout=1200s
```

## 11. Create the primary Ops Manager

No `spec.applicationDatabase` — it references the external multi-cluster AppDB via
`externalApplicationDatabaseRef` (`kind: MongoDBMultiCluster`).

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
  externalApplicationDatabaseRef:
    name: ${APPDB_NAME}
    kind: MongoDBMultiCluster
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

## 12. Wait for the primary Ops Manager

```bash
kubectl wait --for=jsonpath='{.status.opsManager.phase}'=Running \
  om/"${PRIMARY_OM_NAME}" --context "${K8S_CTX_0}" -n "${MDB_NS}" --timeout=1800s

# In external-AppDB mode the operator does not manage an internal AppDB for this OM,
# so its AppDB status is Disabled (this is expected, not an error):
kubectl wait --for=jsonpath='{.status.applicationDatabase.phase}'=Disabled \
  om/"${PRIMARY_OM_NAME}" --context "${K8S_CTX_0}" -n "${MDB_NS}" --timeout=600s
```

## 13. Verify

In multi-cluster mode the AppDB StatefulSets live in the member clusters (one per cluster, named
`<appdb>-<clusterIndex>`), and ownership is tracked by the `mongodbmulticluster` **label**
(`<namespace>-<appdb-name>`) rather than by `ownerReferences`.

```bash
expected_owner="${MDB_NS}-${APPDB_NAME}"
idx=0
for ctx in "${K8S_CTX_1}" "${K8S_CTX_2}"; do
  sts="${APPDB_NAME}-${idx}"
  echo "--- cluster ${ctx} (StatefulSet ${sts}) ---"
  kubectl get pods --context "${ctx}" -n "${MDB_NS}" -l "mongodbmulticluster=${expected_owner}"
  kubectl get statefulset "${sts}" --context "${ctx}" -n "${MDB_NS}" \
    -o jsonpath='{.metadata.labels.mongodbmulticluster}{"\n"}'
  idx=$((idx + 1))
done

# The operator computes the AppDB connection string for the primary OM (central cluster):
kubectl get secret "${APPDB_CONNECTION_STRING_SECRET}" --context "${K8S_CTX_0}" -n "${MDB_NS}" -o name
```

You should see: the management OM and primary OM `Running` on the central cluster; the AppDB members
(`${APPDB_NAME}-0-*` in `K8S_CTX_1`, `${APPDB_NAME}-1-*` in `K8S_CTX_2`) `Running` with the
`mongodbmulticluster` label equal to `${MDB_NS}-${APPDB_NAME}`; and the connection-string secret
present.

## Troubleshooting

- **AppDB agents fail the binary download with `x509: certificate signed by unknown authority`
  (downloading `https://downloads.mongodb.com/...`)** — with AppDB TLS enabled the agent validates
  the download against the `spec.security.tls.ca` bundle, not the system store. The CA ConfigMap must
  include the **public** roots that sign `downloads.mongodb.com`, not only your self-signed CA.
  Rebuild it as in step 5 (self-signed CA **+** the host system CA bundle). The management OM's *own*
  internal AppDB is unaffected because it has TLS disabled and uses the system trust store.
- **`The ConfigMap "<appdb>-ca" is invalid: metadata.annotations: Too long`** — the combined CA
  bundle exceeds the 262144-byte limit of the client-side apply annotation. Apply it with
  `kubectl apply --server-side --force-conflicts` (as in step 5), or `kubectl create` without
  `apply`.
- **AppDB `MongoDBMultiCluster` is `Failed` with `Invalid config: MongoDB version <x> is not
  available`** — the external AppDB is an Ops-Manager-managed MongoDB, so its `spec.version` must be
  a version the **management OM** offers in its manifest. Keep `OM_VERSION` and `APPDB_VERSION` on
  the same major line (e.g. OM `8.0.x` with AppDB `8.0.x-ent`). A `7.0.x` OM does not offer `8.0.x`
  MongoDB.
- **AppDB agents log `Could not resolve host` for the management OM service** — the member-cluster
  namespace is missing the `istio-injection=enabled` label (step 1), so the pods have no sidecar and
  cannot resolve the central-cluster OM service over the mesh.
- **Management OM pod CrashLoops right after start; logs show `mms.ignoreInitialUiSetup ...
  mms.fromEmailAddr must not be blank`** — add the `mms.*` mail block shown in step 7 to
  `spec.configuration`.
- **AppDB stays `Pending` and its agents never get an automation config** — the AppDB CR was created
  before the management OM's public REST API was serving, so the operator could not seed the project.
  Ensure the readiness poll in step 8 passes before step 10.
- **`externalApplicationDatabaseRef.name` rejected** — the referenced CR name must be exactly
  `<primary-om-name>-db`.
- **Primary OM `status.applicationDatabase.phase` is `Disabled`, not `Running`** — expected: in
  external-AppDB mode the operator does not manage the AppDB for this OM.
