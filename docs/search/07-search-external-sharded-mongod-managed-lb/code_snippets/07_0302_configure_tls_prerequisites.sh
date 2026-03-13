#!/usr/bin/env bash
# Configure TLS prerequisites: CA issuer and certificate
#
# This creates:
# 1. A self-signed ClusterIssuer (for bootstrapping)
# 2. A CA Certificate signed by the self-signed issuer
# 3. A CA Issuer that uses the CA certificate to sign other certificates
#
# In production, you would use your own CA or a public CA like Let's Encrypt.

echo "Configuring TLS prerequisites..."

# Create self-signed ClusterIssuer for bootstrapping
kubectl apply --context "${K8S_CTX}" -f - <<EOF
apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata:
  name: ${MDB_TLS_SELF_SIGNED_ISSUER}
spec:
  selfSigned: {}
EOF

echo "  ✓ Self-signed ClusterIssuer created"

# Create CA Certificate (will be used to sign all other certificates)
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

# Wait for CA certificate to be ready
echo "  Waiting for CA certificate..."
kubectl wait --for=condition=Ready certificate/${MDB_TLS_CA_CERT_NAME} \
  -n "${CERT_MANAGER_NAMESPACE}" \
  --context "${K8S_CTX}" \
  --timeout=60s

# Create CA Issuer that uses the CA certificate
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

# Extract the CA certificate from the secret
kubectl get secret "${MDB_TLS_CA_SECRET_NAME}" \
  -n "${CERT_MANAGER_NAMESPACE}" \
  --context "${K8S_CTX}" \
  -o jsonpath='{.data.tls\.crt}' | base64 -d > /tmp/ca.crt

# Create CA ConfigMap in the target namespace (needed by MongoDB Enterprise)
# The Enterprise operator reads CA from ConfigMap with key "ca-pem"
kubectl create configmap "${MDB_TLS_CA_CONFIGMAP}" \
  --from-file=ca-pem=/tmp/ca.crt \
  -n "${MDB_NS}" \
  --context "${K8S_CTX}" \
  --dry-run=client -o yaml | kubectl apply --context "${K8S_CTX}" -f -

echo "  ✓ CA ConfigMap created for MongoDB Enterprise"

# Create CA Secret in the target namespace (needed by MongoDBSearch external source)
# External source expects CA in a Secret with key "ca.crt"
kubectl create secret generic "${MDB_TLS_CA_SECRET_NAME}" \
  --from-file=ca.crt=/tmp/ca.crt \
  -n "${MDB_NS}" \
  --context "${K8S_CTX}" \
  --dry-run=client -o yaml | kubectl apply --context "${K8S_CTX}" -f -

echo "  ✓ CA Secret created for MongoDBSearch external source"

rm -f /tmp/ca.crt

echo "✓ TLS prerequisites configured"
echo "  - Self-signed issuer: ${MDB_TLS_SELF_SIGNED_ISSUER}"
echo "  - CA certificate: ${MDB_TLS_CA_CERT_NAME}"
echo "  - CA issuer: ${MDB_TLS_CA_ISSUER}"
echo "  - CA ConfigMap (for MongoDB): ${MDB_TLS_CA_CONFIGMAP}"
echo "  - CA Secret (for Search): ${MDB_TLS_CA_SECRET_NAME}"
