# MongoDB Search with External Replica Set and External Load Balancer

## Overview

This guide validates MongoDB Search deployment with an **external non-sharded replica set** using an **external L7 load balancer (Envoy)** for high availability. Unlike sharded cluster configurations, this setup uses a single replica set as the data source with **multiple mongot replicas** behind Envoy for search query distribution.

### Key Features Tested

- External MongoDB replica set (3 members) as data source
- Multiple mongot replicas (2-3) for high availability
- Envoy proxy as L7 load balancer distributing traffic across mongot pods
- TLS enabled for all connections (mongod → Envoy → mongot)
- External LB mode configuration in MongoDBSearch

### Architecture

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                           Kubernetes Cluster                                 │
│                                                                             │
│  ┌─────────────────────────────────────────────────────────────────────┐   │
│  │                    External MongoDB Replica Set                      │   │
│  │  ┌──────────┐    ┌──────────┐    ┌──────────┐                       │   │
│  │  │ mongod-0 │    │ mongod-1 │    │ mongod-2 │                       │   │
│  │  │ (Primary)│    │(Secondary│    │(Secondary│                       │   │
│  │  └────┬─────┘    └────┬─────┘    └────┬─────┘                       │   │
│  │       │               │               │                              │   │
│  │       └───────────────┼───────────────┘                              │   │
│  │                       │ mongotHost: envoy-proxy:27029                │   │
│  │                       ▼                                              │   │
│  │              ┌────────────────┐                                      │   │
│  │              │  Envoy Proxy   │ (L7 Load Balancer)                   │   │
│  │              │   Port 27029   │                                      │   │
│  │              └───────┬────────┘                                      │   │
│  │                      │ Round-robin to mongot pods                    │   │
│  │         ┌────────────┼────────────┐                                  │   │
│  │         ▼            ▼            ▼                                  │   │
│  │   ┌──────────┐ ┌──────────┐ ┌──────────┐                            │   │
│  │   │ mongot-0 │ │ mongot-1 │ │ mongot-2 │  (MongoDBSearch replicas)  │   │
│  │   │ Port 27028│ │ Port 27028│ │ Port 27028│                          │   │
│  │   └──────────┘ └──────────┘ └──────────┘                            │   │
│  └─────────────────────────────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────────────────────────────┘
```

### Traffic Flow

1. **Data Sync**: Each mongot replica syncs data from the MongoDB replica set
2. **Search Queries**: mongod sends search queries to Envoy (port 27029)
3. **Load Balancing**: Envoy distributes queries across mongot replicas (round-robin)
4. **Response**: mongot processes query and returns results through Envoy to mongod

---

## Prerequisites

### Required Tools

| Tool | Version | Purpose |
|------|---------|---------|
| `kubectl` | 1.25+ | Kubernetes CLI |
| `helm` | 3.10+ | Helm package manager |
| `openssl` | 1.1+ | TLS certificate generation |
| `curl` | any | Download sample data |

### Required Access

- Kubernetes cluster with admin access
- Ability to create namespaces, deployments, services, secrets
- cert-manager installed (or ability to install it)

### MongoDB Operator

- MongoDB Kubernetes Operator installed (Community or Enterprise)
- Operator version supporting MongoDBSearch CRD

---

## Environment Variables

Set these environment variables before running the commands. Copy this block and modify the values as needed:

```bash
# Kubernetes context - REPLACE with your cluster context
export K8S_CTX="<your-kubernetes-context>"

# Namespace for all resources
export MDB_NS="mongodb"

# MongoDB Community resource name (simulates external replica set)
export MDB_RESOURCE_NAME="mdbc-rs"

# MongoDB Search resource name
export MDB_SEARCH_RESOURCE_NAME="mdbs-search"

# MongoDB version (minimum 8.2.0 required for Search)
export MDB_VERSION="8.2.0"

# Number of mongot replicas for high availability
# This is the key differentiator - multiple mongot pods behind Envoy
export MDB_MONGOT_REPLICAS=3

# User passwords - CHANGE THESE in production
export MDB_ADMIN_USER_PASSWORD="admin-user-password-CHANGE-ME"
export MDB_USER_PASSWORD="mdb-user-password-CHANGE-ME"
export MDB_SEARCH_SYNC_USER_PASSWORD="search-sync-user-password-CHANGE-ME"

# TLS configuration
export MDB_TLS_CA_SECRET_NAME="root-secret"
export MDB_TLS_CA_CONFIGMAP="${MDB_RESOURCE_NAME}-ca"
export MDB_TLS_SERVER_CERT_SECRET_NAME="${MDB_RESOURCE_NAME}-cert"
export MDB_SEARCH_TLS_SECRET_NAME="${MDB_SEARCH_RESOURCE_NAME}-tls"

# cert-manager configuration
export CERT_MANAGER_NAMESPACE="cert-manager"
export MDB_TLS_SELF_SIGNED_ISSUER="selfsigned-bootstrap-issuer"
export MDB_TLS_CA_CERT_NAME="my-selfsigned-ca"
export MDB_TLS_CA_ISSUER="my-ca-issuer"

# Envoy proxy configuration
export ENVOY_IMAGE="envoyproxy/envoy:v1.31-latest"
export ENVOY_PROXY_PORT=27029

# Operator Helm chart
export OPERATOR_HELM_CHART="mongodb/mongodb-kubernetes"
export OPERATOR_ADDITIONAL_HELM_VALUES=""

# External MongoDB hosts (will be set after deployment)
# These point to the simulated external replica set
export MDB_EXTERNAL_HOST_0="${MDB_RESOURCE_NAME}-0.${MDB_RESOURCE_NAME}-svc.${MDB_NS}.svc.cluster.local:27017"
export MDB_EXTERNAL_HOST_1="${MDB_RESOURCE_NAME}-1.${MDB_RESOURCE_NAME}-svc.${MDB_NS}.svc.cluster.local:27017"
export MDB_EXTERNAL_HOST_2="${MDB_RESOURCE_NAME}-2.${MDB_RESOURCE_NAME}-svc.${MDB_NS}.svc.cluster.local:27017"

# Connection string for queries
export MDB_CONNECTION_STRING="mongodb://mdb-user:${MDB_USER_PASSWORD}@${MDB_EXTERNAL_HOST_0},${MDB_EXTERNAL_HOST_1},${MDB_EXTERNAL_HOST_2}/?replicaSet=${MDB_RESOURCE_NAME}&tls=true&tlsCAFile=/tls/ca.crt"
```

---

## Step-by-Step Instructions

### Step 1: Create Namespace

Create the namespace for all MongoDB resources:

```bash
kubectl create namespace "${MDB_NS}" --context "${K8S_CTX}" --dry-run=client -o yaml | \
  kubectl apply --context "${K8S_CTX}" -f -
```

**Expected output:**
```
namespace/mongodb created
```

### Step 2: Install cert-manager

Install cert-manager for TLS certificate management:

```bash
# Add Jetstack Helm repository
helm repo add jetstack https://charts.jetstack.io
helm repo update

# Install cert-manager with CRDs
helm upgrade --install cert-manager jetstack/cert-manager \
  --namespace "${CERT_MANAGER_NAMESPACE}" \
  --create-namespace \
  --kube-context "${K8S_CTX}" \
  --set crds.enabled=true \
  --wait

# Verify cert-manager pods are running
kubectl get pods -n "${CERT_MANAGER_NAMESPACE}" --context "${K8S_CTX}"
```

**Expected output:**
```
NAME                                       READY   STATUS    RESTARTS   AGE
cert-manager-cainjector-xxxxx-xxxxx        1/1     Running   0          30s
cert-manager-xxxxx-xxxxx                   1/1     Running   0          30s
cert-manager-webhook-xxxxx-xxxxx           1/1     Running   0          30s
```

### Step 3: Configure TLS Certificate Authority

Create a self-signed CA issuer and CA certificate:

```bash
# Create self-signed bootstrap issuer
kubectl apply --context "${K8S_CTX}" -n "${CERT_MANAGER_NAMESPACE}" -f - <<EOF
apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata:
  name: ${MDB_TLS_SELF_SIGNED_ISSUER}
spec:
  selfSigned: {}
EOF

# Create CA certificate
kubectl apply --context "${K8S_CTX}" -n "${CERT_MANAGER_NAMESPACE}" -f - <<EOF
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: ${MDB_TLS_CA_CERT_NAME}
spec:
  isCA: true
  commonName: mongodb-ca
  secretName: ${MDB_TLS_CA_SECRET_NAME}
  duration: 87600h # 10 years
  privateKey:
    algorithm: ECDSA
    size: 256
  issuerRef:
    name: ${MDB_TLS_SELF_SIGNED_ISSUER}
    kind: ClusterIssuer
    group: cert-manager.io
EOF

# Create CA issuer using the CA certificate
kubectl apply --context "${K8S_CTX}" -n "${CERT_MANAGER_NAMESPACE}" -f - <<EOF
apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata:
  name: ${MDB_TLS_CA_ISSUER}
spec:
  ca:
    secretName: ${MDB_TLS_CA_SECRET_NAME}
EOF

# Wait for CA certificate to be ready
kubectl wait --context "${K8S_CTX}" -n "${CERT_MANAGER_NAMESPACE}" \
  --for=condition=Ready certificate/${MDB_TLS_CA_CERT_NAME} --timeout=60s
```

**Expected output:**
```
clusterissuer.cert-manager.io/selfsigned-bootstrap-issuer created
certificate.cert-manager.io/my-selfsigned-ca created
clusterissuer.cert-manager.io/my-ca-issuer created
certificate.cert-manager.io/my-selfsigned-ca condition met
```

### Step 4: Generate TLS Certificates

Issue TLS certificates for MongoDB and MongoDBSearch:

```bash
# Create CA ConfigMap in the MongoDB namespace (required by MongoDB Community)
kubectl get secret "${MDB_TLS_CA_SECRET_NAME}" -n "${CERT_MANAGER_NAMESPACE}" --context "${K8S_CTX}" \
  -o jsonpath='{.data.ca\.crt}' | base64 -d > /tmp/ca.crt
kubectl get secret "${MDB_TLS_CA_SECRET_NAME}" -n "${CERT_MANAGER_NAMESPACE}" --context "${K8S_CTX}" \
  -o jsonpath='{.data.tls\.crt}' | base64 -d >> /tmp/ca.crt

kubectl create configmap "${MDB_TLS_CA_CONFIGMAP}" \
  --from-file=ca-pem=/tmp/ca.crt \
  --namespace="${MDB_NS}" --context "${K8S_CTX}" \
  --dry-run=client -o yaml | kubectl apply --context "${K8S_CTX}" -f -

# Issue MongoDB server certificate
kubectl apply --context "${K8S_CTX}" -n "${MDB_NS}" -f - <<EOF
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: ${MDB_TLS_SERVER_CERT_SECRET_NAME}
spec:
  secretName: ${MDB_TLS_SERVER_CERT_SECRET_NAME}
  duration: 8760h # 1 year
  renewBefore: 720h # 30 days
  subject:
    organizations:
      - MongoDB
  commonName: "${MDB_RESOURCE_NAME}"
  dnsNames:
    - "${MDB_RESOURCE_NAME}-0.${MDB_RESOURCE_NAME}-svc.${MDB_NS}.svc.cluster.local"
    - "${MDB_RESOURCE_NAME}-1.${MDB_RESOURCE_NAME}-svc.${MDB_NS}.svc.cluster.local"
    - "${MDB_RESOURCE_NAME}-2.${MDB_RESOURCE_NAME}-svc.${MDB_NS}.svc.cluster.local"
    - "*.${MDB_RESOURCE_NAME}-svc.${MDB_NS}.svc.cluster.local"
    - "*.${MDB_NS}.svc.cluster.local"
  privateKey:
    algorithm: ECDSA
    size: 256
  usages:
    - server auth
    - client auth
  issuerRef:
    name: ${MDB_TLS_CA_ISSUER}
    kind: ClusterIssuer
    group: cert-manager.io
EOF

# Issue MongoDBSearch TLS certificate (shared by all mongot replicas)
kubectl apply --context "${K8S_CTX}" -n "${MDB_NS}" -f - <<EOF
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: ${MDB_SEARCH_TLS_SECRET_NAME}
spec:
  secretName: ${MDB_SEARCH_TLS_SECRET_NAME}
  duration: 8760h
  renewBefore: 720h
  subject:
    organizations:
      - MongoDB
  commonName: "${MDB_SEARCH_RESOURCE_NAME}"
  dnsNames:
    - "${MDB_SEARCH_RESOURCE_NAME}-mongot-0.${MDB_SEARCH_RESOURCE_NAME}-svc.${MDB_NS}.svc.cluster.local"
    - "${MDB_SEARCH_RESOURCE_NAME}-mongot-1.${MDB_SEARCH_RESOURCE_NAME}-svc.${MDB_NS}.svc.cluster.local"
    - "${MDB_SEARCH_RESOURCE_NAME}-mongot-2.${MDB_SEARCH_RESOURCE_NAME}-svc.${MDB_NS}.svc.cluster.local"
    - "*.${MDB_SEARCH_RESOURCE_NAME}-svc.${MDB_NS}.svc.cluster.local"
    - "*.${MDB_NS}.svc.cluster.local"
  privateKey:
    algorithm: ECDSA
    size: 256
  usages:
    - server auth
    - client auth
  issuerRef:
    name: ${MDB_TLS_CA_ISSUER}
    kind: ClusterIssuer
    group: cert-manager.io
EOF

# Wait for certificates to be ready
kubectl wait --context "${K8S_CTX}" -n "${MDB_NS}" \
  --for=condition=Ready certificate/${MDB_TLS_SERVER_CERT_SECRET_NAME} --timeout=60s
kubectl wait --context "${K8S_CTX}" -n "${MDB_NS}" \
  --for=condition=Ready certificate/${MDB_SEARCH_TLS_SECRET_NAME} --timeout=60s
```

**Expected output:**
```
configmap/mdbc-rs-ca created
certificate.cert-manager.io/mdbc-rs-cert created
certificate.cert-manager.io/mdbs-search-tls created
certificate.cert-manager.io/mdbc-rs-cert condition met
certificate.cert-manager.io/mdbs-search-tls condition met
```

### Step 5: Create User Secrets

Create Kubernetes secrets for MongoDB user passwords:

```bash
# Create admin user secret
kubectl create secret generic mdb-admin-user-password \
  --from-literal=password="${MDB_ADMIN_USER_PASSWORD}" \
  --dry-run=client -o yaml | kubectl apply --context "${K8S_CTX}" --namespace "${MDB_NS}" -f -

# Create search sync source user secret
kubectl create secret generic "${MDB_SEARCH_RESOURCE_NAME}-search-sync-source-password" \
  --from-literal=password="${MDB_SEARCH_SYNC_USER_PASSWORD}" \
  --dry-run=client -o yaml | kubectl apply --context "${K8S_CTX}" --namespace "${MDB_NS}" -f -

# Create regular user secret
kubectl create secret generic mdb-user-password \
  --from-literal=password="${MDB_USER_PASSWORD}" \
  --dry-run=client -o yaml | kubectl apply --context "${K8S_CTX}" --namespace "${MDB_NS}" -f -

echo "User secrets created."
```

**Expected output:**
```
secret/mdb-admin-user-password created
secret/mdbs-search-search-sync-source-password created
secret/mdb-user-password created
User secrets created.
```

### Step 6: Deploy MongoDB Community Replica Set

Deploy a MongoDB Community replica set that simulates an external MongoDB source:

```bash
# Set the search hostname (Envoy proxy endpoint)
export MDB_SEARCH_HOSTNAME="envoy-proxy.${MDB_NS}.svc.cluster.local"

kubectl apply --context "${K8S_CTX}" -n "${MDB_NS}" -f - <<EOF
apiVersion: mongodbcommunity.mongodb.com/v1
kind: MongoDBCommunity
metadata:
  name: ${MDB_RESOURCE_NAME}
spec:
  version: ${MDB_VERSION}
  type: ReplicaSet
  members: 3
  security:
    tls:
      enabled: true
      certificateKeySecretRef:
        name: ${MDB_TLS_SERVER_CERT_SECRET_NAME}
      caConfigMapRef:
        name: ${MDB_TLS_CA_CONFIGMAP}
    authentication:
      ignoreUnknownUsers: true
      modes:
        - SCRAM
  additionalMongodConfig:
    setParameter:
      mongotHost: ${MDB_SEARCH_HOSTNAME}:${ENVOY_PROXY_PORT}
      searchIndexManagementHostAndPort: ${MDB_SEARCH_HOSTNAME}:${ENVOY_PROXY_PORT}
      skipAuthenticationToSearchIndexManagementServer: false
      searchTLSMode: requireTLS
      useGrpcForSearch: true
  agent:
    logLevel: DEBUG
  statefulSet:
    spec:
      template:
        spec:
          containers:
            - name: mongod
              resources:
                limits:
                  cpu: "2"
                  memory: 2Gi
                requests:
                  cpu: "1"
                  memory: 1Gi
            - name: mongodb-agent
              resources:
                limits:
                  cpu: "1"
                  memory: 2Gi
                requests:
                  cpu: "0.5"
                  memory: 1Gi
  users:
    - name: mdb-admin
      db: admin
      passwordSecretRef:
        name: mdb-admin-user-password
      scramCredentialsSecretName: mdb-admin-user
      roles:
        - name: root
          db: admin
    - name: mdb-user
      db: admin
      passwordSecretRef:
        name: mdb-user-password
      scramCredentialsSecretName: mdb-user-scram
      roles:
        - name: restore
          db: sample_mflix
        - name: readWrite
          db: sample_mflix
    - name: search-sync-source
      db: admin
      passwordSecretRef:
        name: ${MDB_SEARCH_RESOURCE_NAME}-search-sync-source-password
      scramCredentialsSecretName: ${MDB_SEARCH_RESOURCE_NAME}-search-sync-source
      roles:
        - name: searchCoordinator
          db: admin
EOF

echo "MongoDB Community replica set created. Waiting for it to be ready..."
```

**Expected output:**
```
mongodbcommunity.mongodbcommunity.mongodb.com/mdbc-rs created
MongoDB Community replica set created. Waiting for it to be ready...
```

### Step 7: Wait for MongoDB Community to be Ready

Wait for the MongoDB replica set to reach Running state:

```bash
# Wait for the MongoDB Community resource to be ready
echo "Waiting for MongoDB Community replica set to be ready..."
kubectl wait --context "${K8S_CTX}" -n "${MDB_NS}" \
  --for=jsonpath='{.status.phase}'=Running \
  mongodbcommunity/${MDB_RESOURCE_NAME} --timeout=600s

# Verify all pods are running
kubectl get pods -n "${MDB_NS}" --context "${K8S_CTX}" -l app="${MDB_RESOURCE_NAME}"
```

**Expected output:**
```
mongodbcommunity.mongodbcommunity.mongodb.com/mdbc-rs condition met
NAME         READY   STATUS    RESTARTS   AGE
mdbc-rs-0    2/2     Running   0          2m
mdbc-rs-1    2/2     Running   0          2m
mdbc-rs-2    2/2     Running   0          2m
```

### Step 8: Generate Envoy Proxy Certificates

Create TLS certificates for Envoy proxy (server and client certificates):

```bash
echo "Creating Envoy proxy certificates..."

# Create temp directory for certificate generation
TEMP_DIR=$(mktemp -d)
cd "${TEMP_DIR}"

# Extract CA certificate from ConfigMap
kubectl get configmap "${MDB_TLS_CA_CONFIGMAP}" -n "${MDB_NS}" --context "${K8S_CTX}" \
  -o jsonpath='{.data.ca-pem}' > ca.pem

# Extract the first certificate (the actual issuer CA)
openssl x509 -in ca.pem -out ca-cert.pem

echo "  ✓ CA certificate extracted"

# Get CA private key from the CA secret
kubectl get secret "${MDB_TLS_CA_SECRET_NAME}" -n "${CERT_MANAGER_NAMESPACE}" --context "${K8S_CTX}" \
  -o jsonpath='{.data.tls\.key}' | base64 -d > ca-key.pem

echo "  ✓ CA private key extracted"

# Generate Envoy server certificate (presented to mongod)
echo "Generating Envoy server certificate..."
openssl ecparam -genkey -name prime256v1 -out envoy-server.key

cat > envoy-server.conf <<EOF
[req]
default_bits = 2048
prompt = no
default_md = sha256
req_extensions = req_ext
distinguished_name = dn

[dn]
C = US
ST = New York
L = New York
O = MongoDB
OU = Envoy Proxy
CN = envoy-proxy.${MDB_NS}.svc.cluster.local

[req_ext]
subjectAltName = @alt_names

[alt_names]
DNS.1 = envoy-proxy.${MDB_NS}.svc.cluster.local
DNS.2 = envoy-proxy-svc.${MDB_NS}.svc.cluster.local
DNS.3 = *.${MDB_NS}.svc.cluster.local
IP.1 = 127.0.0.1

[v3_ext]
subjectAltName = @alt_names
keyUsage = critical, digitalSignature, keyEncipherment
extendedKeyUsage = serverAuth, clientAuth
EOF

openssl req -new -key envoy-server.key -out envoy-server.csr -config envoy-server.conf
openssl x509 -req -in envoy-server.csr -CA ca-cert.pem -CAkey ca-key.pem \
  -CAcreateserial -out envoy-server.crt -days 365 \
  -extensions v3_ext -extfile envoy-server.conf

# Create combined PEM (cert + key)
cat envoy-server.crt envoy-server.key > envoy-server-combined.pem
echo "  ✓ Envoy server certificate created"

# Generate Envoy client certificate (presented to mongot)
echo "Generating Envoy client certificate..."
openssl ecparam -genkey -name prime256v1 -out envoy-client.key

cat > envoy-client.conf <<EOF
[req]
default_bits = 2048
prompt = no
default_md = sha256
req_extensions = req_ext
distinguished_name = dn

[dn]
C = US
ST = New York
L = New York
O = MongoDB
OU = Envoy Proxy Client
CN = envoy-proxy-client.${MDB_NS}.svc.cluster.local

[req_ext]
subjectAltName = @alt_names

[alt_names]
DNS.1 = envoy-proxy-client.${MDB_NS}.svc.cluster.local
DNS.2 = *.${MDB_NS}.svc.cluster.local

[v3_ext]
subjectAltName = @alt_names
keyUsage = critical, digitalSignature, keyEncipherment
extendedKeyUsage = clientAuth, serverAuth
EOF

openssl req -new -key envoy-client.key -out envoy-client.csr -config envoy-client.conf
openssl x509 -req -in envoy-client.csr -CA ca-cert.pem -CAkey ca-key.pem \
  -CAcreateserial -out envoy-client.crt -days 365 \
  -extensions v3_ext -extfile envoy-client.conf

# Create combined PEM (cert + key)
cat envoy-client.crt envoy-client.key > envoy-client-combined.pem
echo "  ✓ Envoy client certificate created"

# Create Kubernetes secrets
echo "Creating Kubernetes secrets..."

kubectl create secret generic envoy-server-cert-pem \
  --from-file=cert.pem=envoy-server-combined.pem \
  --namespace="${MDB_NS}" --context "${K8S_CTX}" \
  --dry-run=client -o yaml | kubectl apply --context "${K8S_CTX}" -f -

kubectl create secret generic envoy-client-cert-pem \
  --from-file=cert.pem=envoy-client-combined.pem \
  --namespace="${MDB_NS}" --context "${K8S_CTX}" \
  --dry-run=client -o yaml | kubectl apply --context "${K8S_CTX}" -f -

echo "  ✓ Secrets created: envoy-server-cert-pem, envoy-client-cert-pem"

# Cleanup
cd -
rm -rf "${TEMP_DIR}"

echo "✓ Envoy certificates created successfully"
```

**Expected output:**
```
Creating Envoy proxy certificates...
  ✓ CA certificate extracted
  ✓ CA private key extracted
Generating Envoy server certificate...
  ✓ Envoy server certificate created
Generating Envoy client certificate...
  ✓ Envoy client certificate created
Creating Kubernetes secrets...
secret/envoy-server-cert-pem created
secret/envoy-client-cert-pem created
  ✓ Secrets created: envoy-server-cert-pem, envoy-client-cert-pem
✓ Envoy certificates created successfully
```

### Step 9: Deploy Envoy ConfigMap

Create the Envoy configuration with round-robin load balancing to multiple mongot replicas:

```bash
echo "Generating Envoy ConfigMap for ${MDB_MONGOT_REPLICAS} mongot replicas..."

# Build endpoints for round-robin load balancing to all mongot pods
mongot_endpoints=""
for i in $(seq 0 $((MDB_MONGOT_REPLICAS - 1))); do
  mongot_endpoints="${mongot_endpoints}
            - endpoint:
                address:
                  socket_address:
                    address: ${MDB_SEARCH_RESOURCE_NAME}-mongot-${i}.${MDB_SEARCH_RESOURCE_NAME}-svc.${MDB_NS}.svc.cluster.local
                    port_value: 27028"
done

# Create the ConfigMap
kubectl apply --context "${K8S_CTX}" -n "${MDB_NS}" -f - <<EOF
apiVersion: v1
kind: ConfigMap
metadata:
  name: envoy-config
data:
  envoy.yaml: |
    admin:
      address:
        socket_address:
          address: 0.0.0.0
          port_value: 9901

    static_resources:
      listeners:
      - name: mongod_listener
        address:
          socket_address:
            address: 0.0.0.0
            port_value: ${ENVOY_PROXY_PORT}
        listener_filters:
        - name: envoy.filters.listener.tls_inspector
          typed_config:
            "@type": type.googleapis.com/envoy.extensions.filters.listener.tls_inspector.v3.TlsInspector
        filter_chains:
        - filters:
          - name: envoy.filters.network.http_connection_manager
            typed_config:
              "@type": type.googleapis.com/envoy.extensions.filters.network.http_connection_manager.v3.HttpConnectionManager
              stat_prefix: ingress_mongot
              codec_type: AUTO
              route_config:
                name: mongot_route
                virtual_hosts:
                - name: mongot_backend
                  domains: ["*"]
                  routes:
                  - match:
                      prefix: "/"
                      grpc: {}
                    route:
                      cluster: mongot_cluster
                      timeout: 300s
              http_filters:
              - name: envoy.filters.http.router
                typed_config:
                  "@type": type.googleapis.com/envoy.extensions.filters.http.router.v3.Router
              http2_protocol_options:
                initial_connection_window_size: 1048576
                initial_stream_window_size: 1048576
              stream_idle_timeout: 300s
              request_timeout: 300s
          transport_socket:
            name: envoy.transport_sockets.tls
            typed_config:
              "@type": type.googleapis.com/envoy.extensions.transport_sockets.tls.v3.DownstreamTlsContext
              common_tls_context:
                tls_certificates:
                - certificate_chain:
                    filename: /etc/envoy/tls/server/cert.pem
                  private_key:
                    filename: /etc/envoy/tls/server/cert.pem
                validation_context:
                  trusted_ca:
                    filename: /etc/envoy/tls/ca/ca-pem
                  match_typed_subject_alt_names:
                  - san_type: DNS
                    matcher:
                      suffix: ".${MDB_NS}.svc.cluster.local"
                tls_params:
                  tls_minimum_protocol_version: TLSv1_2
                  tls_maximum_protocol_version: TLSv1_2
                alpn_protocols:
                - "h2"
              require_client_certificate: true

      clusters:
      - name: mongot_cluster
        type: STRICT_DNS
        lb_policy: ROUND_ROBIN
        http2_protocol_options:
          initial_connection_window_size: 1048576
          initial_stream_window_size: 1048576
        load_assignment:
          cluster_name: mongot_cluster
          endpoints:
          - lb_endpoints:${mongot_endpoints}
        circuit_breakers:
          thresholds:
          - priority: DEFAULT
            max_connections: 1024
            max_pending_requests: 1024
            max_requests: 1024
            max_retries: 3
        transport_socket:
          name: envoy.transport_sockets.tls
          typed_config:
            "@type": type.googleapis.com/envoy.extensions.transport_sockets.tls.v3.UpstreamTlsContext
            common_tls_context:
              tls_certificates:
              - certificate_chain:
                  filename: /etc/envoy/tls/client/cert.pem
                private_key:
                  filename: /etc/envoy/tls/client/cert.pem
              validation_context:
                trusted_ca:
                  filename: /etc/envoy/tls/ca/ca-pem
                match_typed_subject_alt_names:
                - san_type: DNS
                  matcher:
                    suffix: ".${MDB_NS}.svc.cluster.local"
              alpn_protocols:
              - "h2"
            sni: ${MDB_SEARCH_RESOURCE_NAME}-svc.${MDB_NS}.svc.cluster.local
        upstream_connection_options:
          tcp_keepalive:
            keepalive_time: 10
            keepalive_interval: 3
            keepalive_probes: 3
        common_http_protocol_options:
          idle_timeout: 300s

    layered_runtime:
      layers:
      - name: static_layer
        static_layer:
          overload:
            global_downstream_max_connections: 50000
EOF

echo "✓ Envoy ConfigMap created with round-robin routing to ${MDB_MONGOT_REPLICAS} mongot replicas"
```

**Expected output:**
```
Generating Envoy ConfigMap for 3 mongot replicas...
configmap/envoy-config created
✓ Envoy ConfigMap created with round-robin routing to 3 mongot replicas
```

### Step 10: Deploy Envoy Proxy

Deploy the Envoy proxy deployment and service:

```bash
kubectl apply --context "${K8S_CTX}" -n "${MDB_NS}" -f - <<EOF
apiVersion: apps/v1
kind: Deployment
metadata:
  name: envoy-proxy
  labels:
    app: envoy-proxy
spec:
  replicas: 1
  selector:
    matchLabels:
      app: envoy-proxy
  template:
    metadata:
      labels:
        app: envoy-proxy
    spec:
      containers:
      - name: envoy
        image: ${ENVOY_IMAGE}
        ports:
        - containerPort: ${ENVOY_PROXY_PORT}
          name: grpc
        - containerPort: 9901
          name: admin
        volumeMounts:
        - name: envoy-config
          mountPath: /etc/envoy
        - name: server-cert
          mountPath: /etc/envoy/tls/server
          readOnly: true
        - name: client-cert
          mountPath: /etc/envoy/tls/client
          readOnly: true
        - name: ca-cert
          mountPath: /etc/envoy/tls/ca
          readOnly: true
        resources:
          limits:
            cpu: "1"
            memory: 512Mi
          requests:
            cpu: "0.5"
            memory: 256Mi
        readinessProbe:
          httpGet:
            path: /ready
            port: 9901
          initialDelaySeconds: 5
          periodSeconds: 10
        livenessProbe:
          httpGet:
            path: /ready
            port: 9901
          initialDelaySeconds: 10
          periodSeconds: 15
      volumes:
      - name: envoy-config
        configMap:
          name: envoy-config
      - name: server-cert
        secret:
          secretName: envoy-server-cert-pem
      - name: client-cert
        secret:
          secretName: envoy-client-cert-pem
      - name: ca-cert
        configMap:
          name: ${MDB_TLS_CA_CONFIGMAP}
---
apiVersion: v1
kind: Service
metadata:
  name: envoy-proxy
spec:
  selector:
    app: envoy-proxy
  ports:
  - name: grpc
    port: ${ENVOY_PROXY_PORT}
    targetPort: ${ENVOY_PROXY_PORT}
  - name: admin
    port: 9901
    targetPort: 9901
  type: ClusterIP
EOF

# Wait for Envoy to be ready
echo "Waiting for Envoy proxy to be ready..."
kubectl rollout status deployment/envoy-proxy -n "${MDB_NS}" --context "${K8S_CTX}" --timeout=120s
```

**Expected output:**
```
deployment.apps/envoy-proxy created
service/envoy-proxy created
Waiting for Envoy proxy to be ready...
deployment "envoy-proxy" successfully rolled out
```

### Step 11: Create MongoDBSearch Resource

Create the MongoDBSearch resource with multiple replicas and external LB mode:

```bash
kubectl apply --context "${K8S_CTX}" -n "${MDB_NS}" -f - <<EOF
apiVersion: mongodb.com/v1
kind: MongoDBSearch
metadata:
  name: ${MDB_SEARCH_RESOURCE_NAME}
spec:
  logLevel: DEBUG
  replicas: ${MDB_MONGOT_REPLICAS}
  source:
    username: search-sync-source
    passwordSecretRef:
      name: ${MDB_SEARCH_RESOURCE_NAME}-search-sync-source-password
      key: password
    external:
      hostAndPorts:
        - ${MDB_EXTERNAL_HOST_0}
        - ${MDB_EXTERNAL_HOST_1}
        - ${MDB_EXTERNAL_HOST_2}
      tls:
        ca:
          name: ${MDB_TLS_CA_SECRET_NAME}
  security:
    tls:
      certificateKeySecretRef:
        name: ${MDB_SEARCH_TLS_SECRET_NAME}
  lb:
    mode: External
    external:
      endpoint: envoy-proxy.${MDB_NS}.svc.cluster.local:${ENVOY_PROXY_PORT}
  resourceRequirements:
    limits:
      cpu: "2"
      memory: 3Gi
    requests:
      cpu: "1"
      memory: 2Gi
EOF

echo "MongoDBSearch resource '${MDB_SEARCH_RESOURCE_NAME}' created with ${MDB_MONGOT_REPLICAS} replicas"
```

**Expected output:**
```
mongodbsearch.mongodb.com/mdbs-search created
MongoDBSearch resource 'mdbs-search' created with 3 replicas
```

### Step 12: Wait for MongoDBSearch to be Ready

Wait for all mongot pods to be running:

```bash
echo "Waiting for MongoDBSearch StatefulSet to be ready..."

# Wait for the StatefulSet to have all replicas ready
kubectl rollout status statefulset/${MDB_SEARCH_RESOURCE_NAME}-mongot \
  -n "${MDB_NS}" --context "${K8S_CTX}" --timeout=600s

# Verify all mongot pods are running
echo ""
echo "MongoDBSearch pods:"
kubectl get pods -n "${MDB_NS}" --context "${K8S_CTX}" \
  -l app.kubernetes.io/name=mongodbsearch,app.kubernetes.io/instance=${MDB_SEARCH_RESOURCE_NAME}

# Check MongoDBSearch status
echo ""
echo "MongoDBSearch status:"
kubectl get mongodbsearch ${MDB_SEARCH_RESOURCE_NAME} -n "${MDB_NS}" --context "${K8S_CTX}" -o wide
```

**Expected output:**
```
Waiting for MongoDBSearch StatefulSet to be ready...
statefulset rolling update complete 3 pods at revision mdbs-search-mongot-xxxxx...

MongoDBSearch pods:
NAME                    READY   STATUS    RESTARTS   AGE
mdbs-search-mongot-0    1/1     Running   0          2m
mdbs-search-mongot-1    1/1     Running   0          2m
mdbs-search-mongot-2    1/1     Running   0          2m

MongoDBSearch status:
NAME          PHASE     AGE
mdbs-search   Running   2m
```

### Step 13: Deploy MongoDB Tools Pod

Deploy a pod with MongoDB tools for data import and queries:

```bash
kubectl apply --context "${K8S_CTX}" -n "${MDB_NS}" -f - <<EOF
apiVersion: v1
kind: Pod
metadata:
  name: mongodb-tools-pod
spec:
  containers:
  - name: mongodb-tools
    image: mongodb/mongodb-community-server:${MDB_VERSION}-ubuntu2204
    command: ["sleep", "infinity"]
    volumeMounts:
    - name: tls-certs
      mountPath: /tls
      readOnly: true
    resources:
      limits:
        cpu: "1"
        memory: 1Gi
      requests:
        cpu: "0.5"
        memory: 512Mi
  volumes:
  - name: tls-certs
    projected:
      sources:
      - configMap:
          name: ${MDB_TLS_CA_CONFIGMAP}
          items:
          - key: ca-pem
            path: ca.crt
EOF

# Wait for the pod to be ready
kubectl wait --context "${K8S_CTX}" -n "${MDB_NS}" \
  --for=condition=Ready pod/mongodb-tools-pod --timeout=120s
```

**Expected output:**
```
pod/mongodb-tools-pod created
pod/mongodb-tools-pod condition met
```

### Step 14: Import Sample Data

Import the sample_mflix database:

```bash
kubectl exec -n "${MDB_NS}" --context "${K8S_CTX}" \
  mongodb-tools-pod -- env MDB_CONNECTION_STRING="${MDB_CONNECTION_STRING}" /bin/bash -eu -c "$(cat <<'EOF'
echo "Downloading sample database archive..."
curl -fSL https://atlas-education.s3.amazonaws.com/sample_mflix.archive -o /tmp/sample_mflix.archive

echo "Restoring sample database..."
mongorestore \
  --archive=/tmp/sample_mflix.archive \
  --verbose=1 \
  --drop \
  --nsInclude 'sample_mflix.*' \
  --uri="${MDB_CONNECTION_STRING}"

echo ""
echo "Verifying data import..."
mongosh "${MDB_CONNECTION_STRING}" --quiet --eval '
  const db = db.getSiblingDB("sample_mflix");
  const collections = db.getCollectionNames();
  print("Collections in sample_mflix: " + collections.join(", "));

  const movieCount = db.movies.countDocuments();
  print("Documents in movies collection: " + movieCount);
'
EOF
)"

echo "Data import complete"
```

**Expected output:**
```
Downloading sample database archive...
Restoring sample database...
...
Verifying data import...
Collections in sample_mflix: comments, movies, sessions, theaters, users
Documents in movies collection: 23530
Data import complete
```

### Step 15: Create Search Index

Create a search index on the movies collection:

```bash
echo "Creating search index..."

kubectl exec -n "${MDB_NS}" --context "${K8S_CTX}" \
  mongodb-tools-pod -- env MDB_CONNECTION_STRING="${MDB_CONNECTION_STRING}" /bin/bash -eu -c "$(cat <<'EOF'
mongosh "${MDB_CONNECTION_STRING}" --eval '
  print("Connecting to MongoDB and creating search index...");
  print("Database: sample_mflix, Collection: movies");

  try {
    // First verify the collection exists
    const collections = db.getSiblingDB("sample_mflix").getCollectionNames();
    print("Collections in sample_mflix: " + collections.join(", "));

    if (!collections.includes("movies")) {
      print("ERROR: movies collection does not exist!");
      quit(1);
    }

    // Check if index already exists
    print("Checking for existing search indexes...");
    const existing = db.getSiblingDB("sample_mflix").runCommand({ listSearchIndexes: "movies" });
    print("listSearchIndexes result: " + JSON.stringify(existing));

    if (existing.ok && existing.cursor && existing.cursor.firstBatch && existing.cursor.firstBatch.length > 0) {
      print("Search index already exists");
    } else {
      print("Creating new search index...");
      const result = db.getSiblingDB("sample_mflix").movies.createSearchIndex(
        "default",
        { mappings: { dynamic: true } }
      );
      print("createSearchIndex result: " + JSON.stringify(result));
      print("Search index created");
    }
  } catch (e) {
    print("ERROR creating search index: " + e.message);
    print("Error stack: " + e.stack);
    quit(1);
  }
'
EOF
)"

echo "Search index creation complete"
```

**Expected output:**
```
Creating search index...
Connecting to MongoDB and creating search index...
Database: sample_mflix, Collection: movies
Collections in sample_mflix: comments, movies, sessions, theaters, users
Checking for existing search indexes...
listSearchIndexes result: {"cursor":{"firstBatch":[],...},"ok":1}
Creating new search index...
createSearchIndex result: "default"
Search index created
Search index creation complete
```

### Step 16: Wait for Search Index to Build

Wait for the search index to be ready:

```bash
echo "Waiting for search index to build (this may take a few minutes)..."

kubectl exec -n "${MDB_NS}" --context "${K8S_CTX}" \
  mongodb-tools-pod -- env MDB_CONNECTION_STRING="${MDB_CONNECTION_STRING}" /bin/bash -eu -c "$(cat <<'EOF'
for i in {1..60}; do
  status=$(mongosh "${MDB_CONNECTION_STRING}" --quiet --eval '
    const result = db.getSiblingDB("sample_mflix").runCommand({ listSearchIndexes: "movies" });
    if (result.ok && result.cursor && result.cursor.firstBatch && result.cursor.firstBatch.length > 0) {
      print(result.cursor.firstBatch[0].status);
    } else {
      print("NOT_FOUND");
    }
  ')

  echo "Index status: ${status}"

  if [ "${status}" = "READY" ]; then
    echo "Search index is ready!"
    exit 0
  fi

  sleep 10
done

echo "Timeout waiting for search index"
exit 1
EOF
)"
```

**Expected output:**
```
Waiting for search index to build (this may take a few minutes)...
Index status: BUILDING
Index status: BUILDING
...
Index status: READY
Search index is ready!
```

### Step 17: Execute Search Queries

Execute search queries to verify the setup:

```bash
echo "Executing search queries..."

echo "=== Test 1: Basic text search ==="
result=$(kubectl exec -n "${MDB_NS}" --context "${K8S_CTX}" \
  mongodb-tools-pod -- env MDB_CONNECTION_STRING="${MDB_CONNECTION_STRING}" /bin/bash -eu -c "$(cat <<'EOF'
mongosh "${MDB_CONNECTION_STRING}" --quiet --eval '
  const results = db.getSiblingDB("sample_mflix").movies.aggregate([
    {
      $search: {
        index: "default",
        text: {
          query: "matrix space dream",
          path: { wildcard: "*" }
        }
      }
    },
    { $limit: 10 },
    { $project: { _id: 0, title: 1, plot: 1, score: { $meta: "searchScore" } } }
  ]).toArray();

  print("Found " + results.length + " results:");
  results.forEach((r, i) => {
    print((i+1) + ". " + r.title + " (score: " + r.score.toFixed(4) + ")");
  });
  print("COUNT:" + results.length);
'
EOF
)")

echo "${result}"
count1=$(echo "${result}" | grep "^COUNT:" | cut -d: -f2)
echo ""

echo "=== Test 2: Wildcard search to verify all documents ==="
result=$(kubectl exec -n "${MDB_NS}" --context "${K8S_CTX}" \
  mongodb-tools-pod -- env MDB_CONNECTION_STRING="${MDB_CONNECTION_STRING}" /bin/bash -eu -c "$(cat <<'EOF'
mongosh "${MDB_CONNECTION_STRING}" --quiet --eval '
  const results = db.getSiblingDB("sample_mflix").movies.aggregate([
    {
      $search: {
        index: "default",
        wildcard: {
          query: "*",
          path: "title",
          allowAnalyzedField: true
        }
      }
    },
    { $project: { _id: 0, title: 1, score: { $meta: "searchScore" } } }
  ]).toArray();

  print("Total documents found via search: " + results.length);

  // Verify we got results (should match total document count)
  const totalDocs = db.getSiblingDB("sample_mflix").movies.countDocuments();
  print("Total documents in collection: " + totalDocs);

  if (results.length === totalDocs) {
    print("SUCCESS: Search returned all documents");
  } else {
    print("WARNING: Search returned " + results.length + " but collection has " + totalDocs);
  }
  print("COUNT:" + results.length);
'
EOF
)")

echo "${result}"
count2=$(echo "${result}" | grep "^COUNT:" | cut -d: -f2)
echo ""

echo "=== Search Query Summary ==="
echo "Test 1 (text search): ${count1:-0} results"
echo "Test 2 (wildcard search): ${count2:-0} results"

if [[ "${count1:-0}" -gt 0 ]] && [[ "${count2:-0}" -gt 0 ]]; then
  echo ""
  echo "SUCCESS: Search queries are working correctly"
else
  echo ""
  echo "ERROR: Search queries failed"
  exit 1
fi
```

**Expected output:**
```
Executing search queries...
=== Test 1: Basic text search ===
Found 10 results:
1. The Matrix (score: 8.1234)
2. The Matrix Reloaded (score: 7.5678)
...

=== Test 2: Wildcard search to verify all documents ===
Total documents found via search: 23530
Total documents in collection: 23530
SUCCESS: Search returned all documents

=== Search Query Summary ===
Test 1 (text search): 10 results
Test 2 (wildcard search): 23530 results

SUCCESS: Search queries are working correctly
```

---

## Verification Steps

### Verify All Components

Run this comprehensive verification:

```bash
echo "=== Verification Summary ==="
echo ""

echo "1. MongoDB Community Replica Set:"
kubectl get mongodbcommunity ${MDB_RESOURCE_NAME} -n "${MDB_NS}" --context "${K8S_CTX}" -o wide
echo ""

echo "2. MongoDB Pods:"
kubectl get pods -n "${MDB_NS}" --context "${K8S_CTX}" -l app="${MDB_RESOURCE_NAME}"
echo ""

echo "3. MongoDBSearch Resource:"
kubectl get mongodbsearch ${MDB_SEARCH_RESOURCE_NAME} -n "${MDB_NS}" --context "${K8S_CTX}" -o wide
echo ""

echo "4. MongoDBSearch Pods (mongot replicas):"
kubectl get pods -n "${MDB_NS}" --context "${K8S_CTX}" \
  -l app.kubernetes.io/name=mongodbsearch,app.kubernetes.io/instance=${MDB_SEARCH_RESOURCE_NAME}
echo ""

echo "5. Envoy Proxy:"
kubectl get deployment,service -n "${MDB_NS}" --context "${K8S_CTX}" -l app=envoy-proxy
echo ""

echo "6. Services:"
kubectl get svc -n "${MDB_NS}" --context "${K8S_CTX}"
echo ""

echo "7. Secrets:"
kubectl get secrets -n "${MDB_NS}" --context "${K8S_CTX}" | grep -E "(tls|cert|password)"
echo ""

echo "8. ConfigMaps:"
kubectl get configmaps -n "${MDB_NS}" --context "${K8S_CTX}"
```

### Verify Traffic Distribution

Check that Envoy is distributing traffic across mongot replicas:

```bash
# Check Envoy admin stats
kubectl exec -n "${MDB_NS}" --context "${K8S_CTX}" \
  deployment/envoy-proxy -- curl -s http://localhost:9901/stats | grep -E "mongot_cluster.*upstream_cx"

# Check Envoy clusters
kubectl exec -n "${MDB_NS}" --context "${K8S_CTX}" \
  deployment/envoy-proxy -- curl -s http://localhost:9901/clusters | grep -E "mongot"
```

### Verify TLS Connections

Verify TLS is working correctly:

```bash
# Check mongot logs for TLS connections
for i in $(seq 0 $((MDB_MONGOT_REPLICAS - 1))); do
  echo "=== mongot-${i} TLS status ==="
  kubectl logs -n "${MDB_NS}" --context "${K8S_CTX}" \
    ${MDB_SEARCH_RESOURCE_NAME}-mongot-${i} --tail=20 | grep -i tls || echo "No TLS logs found"
done

# Check Envoy logs
echo "=== Envoy proxy logs ==="
kubectl logs -n "${MDB_NS}" --context "${K8S_CTX}" \
  deployment/envoy-proxy --tail=30 | grep -i -E "(tls|ssl|connect)" || echo "No TLS logs found"
```

---

## Expected Outputs

### Successful Deployment

| Component | Expected State |
|-----------|----------------|
| MongoDB Community | Phase: Running, 3/3 members ready |
| MongoDBSearch | Phase: Running |
| mongot pods | 3/3 Running |
| Envoy proxy | 1/1 Running |
| Search index | Status: READY |
| Search queries | Return results |

### Key Metrics

- **mongot replicas**: 3 pods running
- **Search index build time**: 2-5 minutes for sample_mflix
- **Query latency**: < 100ms for simple queries
- **Traffic distribution**: Approximately equal across mongot replicas

---

## Troubleshooting

### Common Issues

#### 1. MongoDBSearch pods not starting

**Symptoms**: mongot pods stuck in Pending or CrashLoopBackOff

**Check**:
```bash
kubectl describe pod ${MDB_SEARCH_RESOURCE_NAME}-mongot-0 -n "${MDB_NS}" --context "${K8S_CTX}"
kubectl logs ${MDB_SEARCH_RESOURCE_NAME}-mongot-0 -n "${MDB_NS}" --context "${K8S_CTX}"
```

**Common causes**:
- TLS certificate issues (check certificate SANs)
- Resource constraints (increase CPU/memory limits)
- Secret not found (verify secret names)

#### 2. Search index not building

**Symptoms**: Index status stays at BUILDING or shows errors

**Check**:
```bash
kubectl exec -n "${MDB_NS}" --context "${K8S_CTX}" \
  mongodb-tools-pod -- mongosh "${MDB_CONNECTION_STRING}" --eval '
    db.getSiblingDB("sample_mflix").runCommand({ listSearchIndexes: "movies" })
  '
```

**Common causes**:
- mongot cannot connect to MongoDB (check TLS and network)
- Insufficient resources on mongot pods
- Data sync issues

#### 3. Envoy proxy connection errors

**Symptoms**: Connection refused or TLS handshake failures

**Check**:
```bash
# Check Envoy logs
kubectl logs deployment/envoy-proxy -n "${MDB_NS}" --context "${K8S_CTX}"

# Check Envoy admin interface
kubectl exec -n "${MDB_NS}" --context "${K8S_CTX}" \
  deployment/envoy-proxy -- curl -s http://localhost:9901/clusters
```

**Common causes**:
- Certificate mismatch (verify SANs match service names)
- ConfigMap not mounted correctly
- mongot service not reachable

#### 4. Search queries return no results

**Symptoms**: Queries execute but return empty results

**Check**:
```bash
# Verify data exists
kubectl exec -n "${MDB_NS}" --context "${K8S_CTX}" \
  mongodb-tools-pod -- mongosh "${MDB_CONNECTION_STRING}" --eval '
    db.getSiblingDB("sample_mflix").movies.countDocuments()
  '

# Verify index is READY
kubectl exec -n "${MDB_NS}" --context "${K8S_CTX}" \
  mongodb-tools-pod -- mongosh "${MDB_CONNECTION_STRING}" --eval '
    db.getSiblingDB("sample_mflix").runCommand({ listSearchIndexes: "movies" })
  '
```

**Common causes**:
- Index not in READY state
- Data not imported correctly
- Wrong database/collection name

### Debug Commands

```bash
# View all resources in namespace
kubectl get all -n "${MDB_NS}" --context "${K8S_CTX}"

# Check events for errors
kubectl get events -n "${MDB_NS}" --context "${K8S_CTX}" --sort-by='.lastTimestamp'

# Check operator logs
kubectl logs -n mongodb-operator-system deployment/mongodb-kubernetes-operator --tail=100

# Check mongot container logs
kubectl logs ${MDB_SEARCH_RESOURCE_NAME}-mongot-0 -n "${MDB_NS}" --context "${K8S_CTX}" -c mongot

# Check MongoDB agent logs
kubectl logs ${MDB_RESOURCE_NAME}-0 -n "${MDB_NS}" --context "${K8S_CTX}" -c mongodb-agent
```

---

## Cleanup

Remove all resources created by this test:

```bash
echo "Cleaning up resources..."

# Delete MongoDBSearch
kubectl delete mongodbsearch ${MDB_SEARCH_RESOURCE_NAME} -n "${MDB_NS}" --context "${K8S_CTX}" --ignore-not-found

# Delete MongoDB Community
kubectl delete mongodbcommunity ${MDB_RESOURCE_NAME} -n "${MDB_NS}" --context "${K8S_CTX}" --ignore-not-found

# Delete Envoy proxy
kubectl delete deployment envoy-proxy -n "${MDB_NS}" --context "${K8S_CTX}" --ignore-not-found
kubectl delete service envoy-proxy -n "${MDB_NS}" --context "${K8S_CTX}" --ignore-not-found
kubectl delete configmap envoy-config -n "${MDB_NS}" --context "${K8S_CTX}" --ignore-not-found

# Delete tools pod
kubectl delete pod mongodb-tools-pod -n "${MDB_NS}" --context "${K8S_CTX}" --ignore-not-found

# Delete secrets
kubectl delete secret envoy-server-cert-pem envoy-client-cert-pem -n "${MDB_NS}" --context "${K8S_CTX}" --ignore-not-found
kubectl delete secret mdb-admin-user-password mdb-user-password -n "${MDB_NS}" --context "${K8S_CTX}" --ignore-not-found
kubectl delete secret ${MDB_SEARCH_RESOURCE_NAME}-search-sync-source-password -n "${MDB_NS}" --context "${K8S_CTX}" --ignore-not-found

# Delete certificates
kubectl delete certificate ${MDB_TLS_SERVER_CERT_SECRET_NAME} ${MDB_SEARCH_TLS_SECRET_NAME} -n "${MDB_NS}" --context "${K8S_CTX}" --ignore-not-found

# Delete CA ConfigMap
kubectl delete configmap ${MDB_TLS_CA_CONFIGMAP} -n "${MDB_NS}" --context "${K8S_CTX}" --ignore-not-found

# Optionally delete the namespace (WARNING: deletes everything in the namespace)
# kubectl delete namespace ${MDB_NS} --context "${K8S_CTX}"

echo "Cleanup complete"
```

---

## Summary

This test validates MongoDB Search with:

| Feature | Configuration |
|---------|---------------|
| MongoDB Source | External replica set (3 members) |
| mongot Replicas | 3 (configurable via MDB_MONGOT_REPLICAS) |
| Load Balancer | Envoy proxy with round-robin |
| TLS | Enabled for all connections |
| LB Mode | External |

**Key Differences from Sharded Configuration**:
- Single replica set instead of sharded cluster
- Multiple mongot replicas share the same data source
- Envoy uses round-robin instead of SNI-based routing
- Single endpoint in `spec.lb.external.endpoint` instead of per-shard endpoints
