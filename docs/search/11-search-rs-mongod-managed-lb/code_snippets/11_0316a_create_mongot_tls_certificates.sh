echo "Creating TLS certificate for MongoDB Search (mongot) pods..."

sts_name="${MDB_RESOURCE_NAME}-search"
cert_name="${MDB_TLS_CERT_SECRET_PREFIX}-${sts_name}-cert"
mongot_svc="${sts_name}-svc"
proxy_svc="${MDB_RESOURCE_NAME}-search-proxy-svc"

echo "  Creating certificate: ${cert_name}"
kubectl apply --context "${K8S_CTX}" -n "${MDB_NS}" -f - <<EOF
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: ${cert_name}
spec:
  secretName: ${cert_name}
  duration: 8760h    # 1 year
  renewBefore: 720h  # 30 days
  privateKey:
    algorithm: RSA
    size: 2048
  usages:
    - server auth
    - client auth
  dnsNames:
    - "*.${mongot_svc}.${MDB_NS}.svc.cluster.local"
    - ${proxy_svc}.${MDB_NS}.svc.cluster.local
  issuerRef:
    name: ${MDB_TLS_CA_ISSUER}
    kind: ClusterIssuer
EOF
echo "  ✓ Certificate requested: ${cert_name}"

echo "Waiting for mongot certificate to be ready..."
kubectl wait --for=condition=Ready certificate/"${cert_name}" \
  -n "${MDB_NS}" \
  --context "${K8S_CTX}" \
  --timeout=60s

echo "✓ MongoDB Search (mongot) TLS certificate created"
