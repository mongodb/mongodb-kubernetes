#!/usr/bin/env bash
# AUDIENCE: internal
# Configure TLS prerequisites: self-signed CA, ClusterIssuer, and CA distribution
#
# cert-manager needs 3 objects to issue certificates:
#   1. Self-signed ClusterIssuer  — bootstraps the CA (can only sign its own cert)
#   2. CA Certificate             — the actual root CA, signed by step 1
#   3. CA ClusterIssuer           — uses the CA from step 2 to sign all other certs

echo "Configuring TLS prerequisites..."

# Step 1: Create self-signed ClusterIssuer for bootstrapping
kubectl apply --context "${K8S_CTX}" -f - <<EOF
apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata:
  name: ${MDB_TLS_SELF_SIGNED_ISSUER}
spec:
  selfSigned: {}
EOF

echo "  ✓ Self-signed ClusterIssuer created"

# Step 2: Create CA Certificate (isCA: true, 10 year validity)
kubectl apply --context "${K8S_CTX}" -n "${CERT_MANAGER_NAMESPACE}" -f - <<EOF
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: ${MDB_TLS_CA_CERT_NAME}
spec:
  isCA: true
  commonName: mongodb-ca
  secretName: ${MDB_TLS_CA_SECRET_NAME}
  duration: 87600h  # 10 years
  renewBefore: 8760h  # 1 year
  privateKey:
    algorithm: ECDSA
    size: 256
  issuerRef:
    name: ${MDB_TLS_SELF_SIGNED_ISSUER}
    kind: ClusterIssuer
EOF

echo "  ✓ CA Certificate requested"

echo "  Waiting for CA certificate..."
kubectl wait --for=condition=Ready certificate/"${MDB_TLS_CA_CERT_NAME}" \
  -n "${CERT_MANAGER_NAMESPACE}" \
  --context "${K8S_CTX}" \
  --timeout=60s

# Step 3: Create CA ClusterIssuer — all subsequent certificates reference this issuer
kubectl apply --context "${K8S_CTX}" -n "${CERT_MANAGER_NAMESPACE}" -f - <<EOF
apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata:
  name: ${MDB_TLS_CA_ISSUER}
spec:
  ca:
    secretName: ${MDB_TLS_CA_SECRET_NAME}
EOF

echo "  ✓ CA Issuer created"

# Distribute CA certificate to target namespace
# MongoDB Enterprise expects CA in a ConfigMap (key "ca-pem")
# MongoDBSearch expects CA in a Secret (key "ca.crt")
echo "  Distributing CA certificate to namespace '${MDB_NS}'..."

ca_tmp=$(mktemp)
trap 'rm -f "${ca_tmp}"' EXIT

kubectl get secret "${MDB_TLS_CA_SECRET_NAME}" \
  -n "${CERT_MANAGER_NAMESPACE}" \
  --context "${K8S_CTX}" \
  -o jsonpath='{.data.tls\.crt}' | base64 -d > "${ca_tmp}"

kubectl create configmap "${MDB_TLS_CA_CONFIGMAP}" \
  --from-file=ca-pem="${ca_tmp}" \
  -n "${MDB_NS}" \
  --context "${K8S_CTX}" \
  --dry-run=client -o yaml | kubectl apply --context "${K8S_CTX}" -f -

kubectl create secret generic "${MDB_TLS_CA_SECRET_NAME}" \
  --from-file=ca.crt="${ca_tmp}" \
  -n "${MDB_NS}" \
  --context "${K8S_CTX}" \
  --dry-run=client -o yaml | kubectl apply --context "${K8S_CTX}" -f -

echo "✓ TLS prerequisites configured"
