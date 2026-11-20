# AppDB server certificate. The operator convention is that the cert secret is named
# "<certsSecretPrefix>-<appdb-name>-cert". The wildcard dnsName covers every per-member AppDB
# Service FQDN (<appdb>-<clusterIndex>-<memberIndex>-svc.<ns>.svc.cluster.local).
echo "Generating the AppDB TLS certificate..."

kubectl apply --context "${K8S_CTX_0}" -n "${MDB_NS}" -f - <<EOF
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: ${APPDB_CERT_PREFIX}-${APPDB_NAME}-cert
spec:
  secretName: ${APPDB_CERT_PREFIX}-${APPDB_NAME}-cert
  duration: 8760h
  renewBefore: 720h
  privateKey:
    algorithm: RSA
    size: 2048
  usages:
    - server auth
    - client auth
  dnsNames:
    - "*.${MDB_NS}.svc.cluster.local"
  issuerRef:
    name: ${APPDB_TLS_CA_ISSUER}
    kind: ClusterIssuer
EOF

echo "Waiting for the AppDB certificate to be ready..."
kubectl wait --for=condition=Ready certificate/"${APPDB_CERT_PREFIX}-${APPDB_NAME}-cert" \
  -n "${MDB_NS}" --context "${K8S_CTX_0}" --timeout=120s

echo "[ok] AppDB TLS certificate '${APPDB_CERT_PREFIX}-${APPDB_NAME}-cert' created"
