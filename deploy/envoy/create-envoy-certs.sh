#!/bin/bash

# Script to create Envoy proxy certificates using the existing MongoDB CA
# This creates certificates with SANs for all proxy services (SNI-based routing)

set -e

NAMESPACE="${NAMESPACE:-ls}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(cd "${SCRIPT_DIR}/../.." && pwd)"

echo "================================================"
echo "  Creating Envoy Proxy Certificates"
echo "  Namespace: ${NAMESPACE}"
echo "  SNI-based routing for multiple replica sets"
echo "================================================"
echo ""

# Verify CA ConfigMap exists
if ! kubectl get configmap issuer-ca -n "${NAMESPACE}" &> /dev/null; then
    echo "ERROR: ConfigMap 'issuer-ca' not found in namespace '${NAMESPACE}'."
    echo "This ConfigMap should contain the MongoDB CA certificate."
    exit 1
fi

# Create temp directory in current directory
TEMP_DIR="${SCRIPT_DIR}/tmp-envoy-certs"
mkdir -p "${TEMP_DIR}"

cd "${TEMP_DIR}"

echo "[1/4] Extracting CA certificate from ConfigMap..."
kubectl get configmap issuer-ca -n "${NAMESPACE}" -o jsonpath='{.data.ca-pem}' > ca.pem

# Extract the first certificate (the actual issuer CA, not the chain)
openssl x509 -in ca.pem -out ca-cert.pem

echo "  ✓ CA certificate extracted"

# Verify CA cert
CA_SUBJECT=$(openssl x509 -in ca-cert.pem -noout -subject)
CA_DATES=$(openssl x509 -in ca-cert.pem -noout -dates)
echo "  CA Subject: ${CA_SUBJECT}"
echo "  CA Validity: ${CA_DATES}"
echo ""

echo "[2/4] Generating Envoy server certificate (for mongod → Envoy)..."
echo "  Including SANs for SNI-based routing to multiple replica sets"

# Generate Envoy server private key (ECDSA P-256 for performance)
openssl ecparam -genkey -name prime256v1 -out envoy-server.key

# Create certificate signing request with SANs for all proxy services
# Note: X.509 wildcards like *-proxy-svc don't work (wildcard must be entire leftmost label)
# So we list all specific proxy service names
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
CN = envoy-proxy.${NAMESPACE}.svc.cluster.local

[req_ext]
subjectAltName = @alt_names

[alt_names]
# Proxy services for each replica set (SNI routing targets)
DNS.1 = mdb-rs-1-proxy-svc.${NAMESPACE}.svc.cluster.local
DNS.2 = mdb-rs-2-proxy-svc.${NAMESPACE}.svc.cluster.local
# Additional proxy services for future replica sets
DNS.3 = mdb-rs-3-proxy-svc.${NAMESPACE}.svc.cluster.local
DNS.4 = mdb-rs-4-proxy-svc.${NAMESPACE}.svc.cluster.local
DNS.5 = mdb-rs-5-proxy-svc.${NAMESPACE}.svc.cluster.local
# Wildcard for the namespace (covers all services if client accepts)
DNS.6 = *.${NAMESPACE}.svc.cluster.local
# Localhost for testing
IP.1 = 127.0.0.1

[v3_ext]
subjectAltName = @alt_names
keyUsage = critical, digitalSignature, keyEncipherment
extendedKeyUsage = serverAuth, clientAuth
EOF

openssl req -new -key envoy-server.key -out envoy-server.csr -config envoy-server.conf

# Get CA key from fixtures
CA_KEY="ca-key.pem"
if [ -f "${PROJECT_DIR}/docker/mongodb-kubernetes-tests/tests/opsmanager/fixtures/ca-tls.key" ]; then
    cp "${PROJECT_DIR}/docker/mongodb-kubernetes-tests/tests/opsmanager/fixtures/ca-tls.key" ca-key.pem
fi

# Sign the certificate
if [ -f "${CA_KEY}" ]; then
    echo "  Found CA key, signing certificate..."
    openssl x509 -req -in envoy-server.csr -CA ca-cert.pem -CAkey "${CA_KEY}" \
      -CAcreateserial -out envoy-server.crt -days 365 \
      -extensions v3_ext -extfile envoy-server.conf
    echo "  ✓ Envoy server certificate signed"

    # Show SANs
    echo "  Server certificate SANs:"
    openssl x509 -in envoy-server.crt -noout -ext subjectAltName | sed 's/^/    /'
else
    echo "  ⚠️  CA private key not found at ${CA_KEY}"
    echo "  ⚠️  Looked for: ${PROJECT_DIR}/docker/mongodb-kubernetes-tests/tests/opsmanager/fixtures/ca-tls.key"
    exit 1
fi

# Create combined PEM file (cert + key) - MongoDB operator format
cat envoy-server.crt envoy-server.key > envoy-server-combined.pem

echo "  ✓ Combined PEM created: envoy-server-combined.pem"
echo ""

echo "[3/4] Generating Envoy client certificate (for Envoy → mongot)..."

# Generate Envoy client private key
openssl ecparam -genkey -name prime256v1 -out envoy-client.key

# Create certificate signing request for client cert
# This cert is used for all upstream connections to mongot services
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
CN = envoy-proxy-client.${NAMESPACE}.svc.cluster.local

[req_ext]
subjectAltName = @alt_names

[alt_names]
DNS.1 = envoy-proxy-client.${NAMESPACE}.svc.cluster.local
# Wildcard to allow connection to any search service
DNS.2 = *.${NAMESPACE}.svc.cluster.local

[v3_ext]
subjectAltName = @alt_names
keyUsage = critical, digitalSignature, keyEncipherment
extendedKeyUsage = clientAuth, serverAuth
EOF

openssl req -new -key envoy-client.key -out envoy-client.csr -config envoy-client.conf

# Sign with CA
if [ -f "${CA_KEY}" ]; then
    openssl x509 -req -in envoy-client.csr -CA ca-cert.pem -CAkey "${CA_KEY}" \
      -CAcreateserial -out envoy-client.crt -days 365 \
      -extensions v3_ext -extfile envoy-client.conf
    echo "  ✓ Envoy client certificate signed"
else
    echo "  ⚠️  CA key not available, cannot sign client certificate"
    exit 1
fi

# Create combined PEM file (cert + key)
cat envoy-client.crt envoy-client.key > envoy-client-combined.pem

echo "  ✓ Combined PEM created: envoy-client-combined.pem"
echo ""

echo "[4/4] Creating Kubernetes secrets..."

# Create Envoy server certificate secret
kubectl create secret generic envoy-server-cert-pem \
  --from-file=cert.pem=envoy-server-combined.pem \
  --namespace="${NAMESPACE}" \
  --dry-run=client -o yaml | kubectl apply -f -

echo "  ✓ Secret created: envoy-server-cert-pem"

# Create Envoy client certificate secret
kubectl create secret generic envoy-client-cert-pem \
  --from-file=cert.pem=envoy-client-combined.pem \
  --namespace="${NAMESPACE}" \
  --dry-run=client -o yaml | kubectl apply -f -

echo "  ✓ Secret created: envoy-client-cert-pem"

echo ""
echo "================================================"
echo "  Certificate Creation Complete!"
echo "================================================"
echo ""
echo "Created secrets in namespace '${NAMESPACE}':"
echo "  • envoy-server-cert-pem (Envoy server cert for mongod connections)"
echo "  • envoy-client-cert-pem (Envoy client cert for mongot connections)"
echo ""
echo "Server certificate SANs (for SNI routing):"
echo "  • mdb-rs-1-proxy-svc.${NAMESPACE}.svc.cluster.local"
echo "  • mdb-rs-2-proxy-svc.${NAMESPACE}.svc.cluster.local"
echo "  • mdb-rs-3-proxy-svc.${NAMESPACE}.svc.cluster.local"
echo "  • mdb-rs-4-proxy-svc.${NAMESPACE}.svc.cluster.local"
echo "  • mdb-rs-5-proxy-svc.${NAMESPACE}.svc.cluster.local"
echo "  • *.${NAMESPACE}.svc.cluster.local (wildcard)"
echo ""
echo "Next steps:"
echo "  1. Deploy Envoy:"
echo "     kubectl apply -f envoy-configmap.yaml -f envoy-deployment.yaml -f proxy-services.yaml"
echo "  2. Verify deployment:"
echo "     kubectl get pods -n ${NAMESPACE} -l app=envoy-proxy"
echo "  3. Check logs:"
echo "     kubectl logs -n ${NAMESPACE} -l app=envoy-proxy"
echo ""
