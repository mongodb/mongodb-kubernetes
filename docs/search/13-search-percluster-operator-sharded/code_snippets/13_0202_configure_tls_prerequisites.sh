echo "Configuring TLS prerequisites (self-signed bootstrap CA chain, cluster 0)..."

kubectl apply --context "${K8S_CLUSTER_0_CONTEXT_NAME}" -f - <<EOF
apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata:
  name: ${MDB_TLS_SELF_SIGNED_ISSUER}
spec:
  selfSigned: {}
EOF

echo "  [ok] Self-signed ClusterIssuer created"

kubectl apply --context "${K8S_CLUSTER_0_CONTEXT_NAME}" -n "${CERT_MANAGER_NAMESPACE}" -f - <<EOF
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

echo "  [ok] CA Certificate requested"

kubectl wait --for=condition=Ready certificate/"${MDB_TLS_CA_CERT_NAME}" \
  -n "${CERT_MANAGER_NAMESPACE}" \
  --context "${K8S_CLUSTER_0_CONTEXT_NAME}" \
  --timeout=60s

kubectl apply --context "${K8S_CLUSTER_0_CONTEXT_NAME}" -n "${CERT_MANAGER_NAMESPACE}" -f - <<EOF
apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata:
  name: ${MDB_TLS_CA_ISSUER}
spec:
  ca:
    secretName: ${MDB_TLS_CA_SECRET_NAME}
EOF

echo "  [ok] CA Issuer created"
echo "[ok] TLS prerequisites configured"
