#!/usr/bin/env bash
# Configure TLS prerequisites: CA issuer and certificate
#
# ============================================================================
# WHAT THIS SCRIPT CREATES
# ============================================================================
# 1. Self-signed ClusterIssuer - A bootstrap issuer that can sign itself
# 2. CA Certificate           - A certificate authority signed by the bootstrap issuer
# 3. CA ClusterIssuer         - An issuer that uses our CA to sign all other certs
# 4. CA ConfigMap             - CA cert for MongoDB Enterprise (uses key "ca-pem")
# 5. CA Secret                - CA cert for MongoDBSearch external (uses key "ca.crt")
#
# ============================================================================
# WHY TWO CA DISTRIBUTIONS (ConfigMap AND Secret)?
# ============================================================================
# MongoDB Enterprise operator expects CA in a ConfigMap with key "ca-pem"
# MongoDBSearch external source expects CA in a Secret with key "ca.crt"
# We create both to satisfy both requirements.
#
# ============================================================================
# IN PRODUCTION
# ============================================================================
# You would replace the self-signed CA with:
# - Your organization's internal CA
# - A public CA like Let's Encrypt
# - A cloud provider's certificate service (AWS ACM, GCP CAS, etc.)
# ============================================================================
# DEPENDS ON: 07_0301_install_cert_manager.sh (cert-manager must be running)
# ============================================================================

echo "Configuring TLS prerequisites..."

# ============================================================================
# STEP 1: Create self-signed ClusterIssuer for bootstrapping
# ============================================================================
echo ""
echo "Step 1: Creating self-signed ClusterIssuer..."
kubectl apply --context "${K8S_CTX}" -f - <<EOF
apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata:
  name: ${MDB_TLS_SELF_SIGNED_ISSUER}
spec:
  selfSigned: {}
EOF

echo "  ✓ Self-signed ClusterIssuer created"

# ============================================================================
# STEP 2: Create CA Certificate (isCA: true, 10 year validity)
# ============================================================================
echo ""
echo "Step 2: Creating CA Certificate..."
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

# ============================================================================
# STEP 3: Create CA ClusterIssuer
# ============================================================================
# This issuer uses our CA certificate to sign all other certificates.
# All subsequent certificates (shards, mongot, envoy) will reference this issuer.
# ============================================================================
echo ""
echo "Step 3: Creating CA ClusterIssuer..."
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

# ============================================================================
# STEP 4: Distribute CA certificate to target namespace
# ============================================================================
# cert-manager stores the CA in the cert-manager namespace, but MongoDB and
# MongoDBSearch need it in their namespace. We extract and copy it.
#
# jsonpath='{.data.tls\.crt}' - Get the base64-encoded certificate
# base64 -d                   - Decode it to PEM format
# ============================================================================
echo ""
echo "Step 4: Distributing CA certificate to namespace '${MDB_NS}'..."

# Extract the CA certificate from cert-manager's secret
kubectl get secret "${MDB_TLS_CA_SECRET_NAME}" \
  -n "${CERT_MANAGER_NAMESPACE}" \
  --context "${K8S_CTX}" \
  -o jsonpath='{.data.tls\.crt}' | base64 -d > /tmp/ca.crt

# Create CA ConfigMap for MongoDB Enterprise operator (expects key "ca-pem")
kubectl create configmap "${MDB_TLS_CA_CONFIGMAP}" \
  --from-file=ca-pem=/tmp/ca.crt \
  -n "${MDB_NS}" \
  --context "${K8S_CTX}" \
  --dry-run=client -o yaml | kubectl apply --context "${K8S_CTX}" -f -

echo "  ✓ CA ConfigMap created for MongoDB Enterprise"

# Create CA Secret for MongoDBSearch external source (expects key "ca.crt")
kubectl create secret generic "${MDB_TLS_CA_SECRET_NAME}" \
  --from-file=ca.crt=/tmp/ca.crt \
  -n "${MDB_NS}" \
  --context "${K8S_CTX}" \
  --dry-run=client -o yaml | kubectl apply --context "${K8S_CTX}" -f -

echo "  ✓ CA Secret created for MongoDBSearch external source"

# Clean up temporary file
rm -f /tmp/ca.crt

echo "✓ TLS prerequisites configured"
echo "  - Self-signed issuer: ${MDB_TLS_SELF_SIGNED_ISSUER}"
echo "  - CA certificate: ${MDB_TLS_CA_CERT_NAME}"
echo "  - CA issuer: ${MDB_TLS_CA_ISSUER}"
echo "  - CA ConfigMap (for MongoDB): ${MDB_TLS_CA_CONFIGMAP}"
echo "  - CA Secret (for Search): ${MDB_TLS_CA_SECRET_NAME}"
