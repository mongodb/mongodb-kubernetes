# Create TLS certificates for Envoy proxy
#
# Envoy needs two sets of certificates:
# 1. Server certificate - presented to mongod when mongod connects to Envoy
#    Must have SANs for all per-shard proxy service names
# 2. Client certificate - presented to mongot when Envoy connects to mongot
#
# Both certificates are signed by the same CA used for MongoDB TLS

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

# Build SANs for server certificate (all per-shard proxy services)
san_dns=""
for i in $(seq 0 $((MDB_SHARD_COUNT - 1))); do
  shard_name="${MDB_RESOURCE_NAME}-${i}"
  proxy_svc="${MDB_RESOURCE_NAME}-mongot-${shard_name}-proxy-svc"
  san_dns="${san_dns}DNS.$((i + 1)) = ${proxy_svc}.${MDB_NS}.svc.cluster.local
"
done
# Add wildcard SAN for flexibility
san_dns="${san_dns}DNS.$((MDB_SHARD_COUNT + 1)) = *.${MDB_NS}.svc.cluster.local"

# Generate Envoy server certificate
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
${san_dns}
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

# Generate Envoy client certificate
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

