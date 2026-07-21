echo "Generating TLS certificates for the sharded source MongoDB (cluster 0)..."

for shard_name in "${MDB_SHARD_0_NAME}" "${MDB_SHARD_1_NAME}"; do
  echo "Creating certificate for shard ${shard_name}..."
  kubectl apply --context "${K8S_CLUSTER_0_CONTEXT_NAME}" -n "${MDB_NAMESPACE}" -f - <<EOF
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: ${MDB_TLS_CERT_SECRET_PREFIX}-${shard_name}-cert
spec:
  secretName: ${MDB_TLS_CERT_SECRET_PREFIX}-${shard_name}-cert
  duration: 8760h    # 1 year
  renewBefore: 720h  # 30 days
  privateKey:
    algorithm: RSA
    size: 2048
  usages:
    - server auth
    - client auth
  dnsNames:
    - "*.${MDB_RESOURCE_NAME}-sh.${MDB_NAMESPACE}.svc.cluster.local"
  issuerRef:
    name: ${MDB_TLS_CA_ISSUER}
    kind: ClusterIssuer
EOF
done

echo "Creating config server certificate..."
kubectl apply --context "${K8S_CLUSTER_0_CONTEXT_NAME}" -n "${MDB_NAMESPACE}" -f - <<EOF
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: ${MDB_TLS_CERT_SECRET_PREFIX}-${MDB_RESOURCE_NAME}-config-cert
spec:
  secretName: ${MDB_TLS_CERT_SECRET_PREFIX}-${MDB_RESOURCE_NAME}-config-cert
  duration: 8760h
  renewBefore: 720h
  privateKey:
    algorithm: RSA
    size: 2048
  usages:
    - server auth
    - client auth
  dnsNames:
    - "*.${MDB_RESOURCE_NAME}-cs.${MDB_NAMESPACE}.svc.cluster.local"
  issuerRef:
    name: ${MDB_TLS_CA_ISSUER}
    kind: ClusterIssuer
EOF

echo "Creating mongos certificate..."
kubectl apply --context "${K8S_CLUSTER_0_CONTEXT_NAME}" -n "${MDB_NAMESPACE}" -f - <<EOF
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: ${MDB_TLS_CERT_SECRET_PREFIX}-${MDB_RESOURCE_NAME}-mongos-cert
spec:
  secretName: ${MDB_TLS_CERT_SECRET_PREFIX}-${MDB_RESOURCE_NAME}-mongos-cert
  duration: 8760h
  renewBefore: 720h
  privateKey:
    algorithm: RSA
    size: 2048
  usages:
    - server auth
    - client auth
  dnsNames:
    - "*.${MDB_RESOURCE_NAME}-svc.${MDB_NAMESPACE}.svc.cluster.local"
    - "${MDB_RESOURCE_NAME}-svc.${MDB_NAMESPACE}.svc.cluster.local"
  issuerRef:
    name: ${MDB_TLS_CA_ISSUER}
    kind: ClusterIssuer
EOF

echo "Waiting for all source TLS certificates to be ready..."
for cert_name in \
    "${MDB_TLS_CERT_SECRET_PREFIX}-${MDB_SHARD_0_NAME}-cert" \
    "${MDB_TLS_CERT_SECRET_PREFIX}-${MDB_SHARD_1_NAME}-cert" \
    "${MDB_TLS_CERT_SECRET_PREFIX}-${MDB_RESOURCE_NAME}-config-cert" \
    "${MDB_TLS_CERT_SECRET_PREFIX}-${MDB_RESOURCE_NAME}-mongos-cert"; do
  kubectl wait --for=condition=Ready certificate/"${cert_name}" \
    -n "${MDB_NAMESPACE}" --context "${K8S_CLUSTER_0_CONTEXT_NAME}" --timeout=60s
done

echo "[ok] All sharded source TLS certificates created"
