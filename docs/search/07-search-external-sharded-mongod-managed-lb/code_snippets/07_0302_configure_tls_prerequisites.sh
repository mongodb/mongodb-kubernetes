#!/usr/bin/env bash
# Configure TLS prerequisites: self-signed CA, ClusterIssuer, and CA distribution

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

# Create CA Certificate (isCA: true, 10 year validity)
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
kubectl wait --for=condition=Ready certificate/${MDB_TLS_CA_CERT_NAME} \
  -n "${CERT_MANAGER_NAMESPACE}" \
  --context "${K8S_CTX}" \
  --timeout=60s

# Create CA ClusterIssuer — all subsequent certificates reference this issuer
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

kubectl get secret "${MDB_TLS_CA_SECRET_NAME}" \
  -n "${CERT_MANAGER_NAMESPACE}" \
  --context "${K8S_CTX}" \
  -o jsonpath='{.data.tls\.crt}' | base64 -d > /tmp/ca.crt

kubectl create configmap "${MDB_TLS_CA_CONFIGMAP}" \
  --from-file=ca-pem=/tmp/ca.crt \
  -n "${MDB_NS}" \
  --context "${K8S_CTX}" \
  --dry-run=client -o yaml | kubectl apply --context "${K8S_CTX}" -f -

kubectl create secret generic "${MDB_TLS_CA_SECRET_NAME}" \
  --from-file=ca.crt=/tmp/ca.crt \
  -n "${MDB_NS}" \
  --context "${K8S_CTX}" \
  --dry-run=client -o yaml | kubectl apply --context "${K8S_CTX}" -f -

rm -f /tmp/ca.crt

echo "✓ TLS prerequisites configured"
