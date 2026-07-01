echo "Creating TLS certificate for MongoDB Search (mongot) pods..."

# One certificate is shared by the per-cluster mongot StatefulSets. It is
# issued on the central cluster and replicated to the member clusters by
# 12_0317 (the operator does not auto-replicate Search Secrets). A wildcard
# SAN covers every per-cluster mongot Service and proxy-Service FQDN.
cert_name="${MDB_TLS_CERT_SECRET_PREFIX}-${MDB_SEARCH_RESOURCE_NAME}-search-cert"

echo "  Creating certificate: ${cert_name}"
kubectl apply --context "${K8S_CTX_0}" -n "${MDB_NS}" -f - <<EOF
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
    - "*.${MDB_NS}.svc.cluster.local"
  issuerRef:
    name: ${MDB_TLS_CA_ISSUER}
    kind: ClusterIssuer
EOF
echo "  [ok] Certificate requested: ${cert_name}"

echo "Waiting for mongot certificate to be ready..."
kubectl wait --for=condition=Ready certificate/"${cert_name}" \
  -n "${MDB_NS}" \
  --context "${K8S_CTX_0}" \
  --timeout=60s

echo "[ok] MongoDB Search (mongot) TLS certificate created"
