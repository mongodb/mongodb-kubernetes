echo "Generating TLS certificate for the MongoDB replica set..."

# The certificate is issued only on the central cluster: the MongoDB controller
# replicates the resulting Secret to the member clusters automatically. A
# wildcard SAN covers every per-pod Service FQDN in the namespace (e.g.
# ${MDB_RESOURCE_NAME}-0-0-svc, ${MDB_RESOURCE_NAME}-1-0-svc).
echo "Creating certificate for RS ${MDB_RESOURCE_NAME}..."
kubectl apply --context "${K8S_CTX_0}" -n "${MDB_NS}" -f - <<EOF
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: ${MDB_TLS_CERT_SECRET_PREFIX}-${MDB_RESOURCE_NAME}-cert
spec:
  secretName: ${MDB_TLS_CERT_SECRET_PREFIX}-${MDB_RESOURCE_NAME}-cert
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
echo "  [ok] Certificate requested for RS ${MDB_RESOURCE_NAME}"

echo ""
echo "Waiting for certificate to be ready..."
kubectl wait --for=condition=Ready certificate/"${MDB_TLS_CERT_SECRET_PREFIX}-${MDB_RESOURCE_NAME}-cert" \
  -n "${MDB_NS}" \
  --context "${K8S_CTX_0}" \
  --timeout=60s

echo "[ok] MongoDB RS TLS certificate created"
