echo "Creating TLS certificates for mongot (per cluster, per shard)..."

# cert-manager only runs on cluster 0, so every Certificate is applied there --
# regardless of which cluster index the resulting secret is named for. Secrets
# for cluster index 1 are copied over to cluster 1 in the next step.
cert_pfx="${MDBS_TLS_CERT_SECRET_PREFIX}-${MDBS_RESOURCE_NAME}"

for cluster_idx in "${MDBS_CLUSTER_0_INDEX}" "${MDBS_CLUSTER_1_INDEX}"; do
  for shard_name in "${MDB_SHARD_0_NAME}" "${MDB_SHARD_1_NAME}"; do
    sts="${MDBS_RESOURCE_NAME}-search-${cluster_idx}-${shard_name}"
    secret_name="${cert_pfx}-search-${cluster_idx}-${shard_name}-cert"

    echo "  Creating certificate: ${secret_name}"
    kubectl apply --context "${K8S_CLUSTER_0_CONTEXT_NAME}" -n "${MDB_NAMESPACE}" -f - <<EOF
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: ${secret_name}
spec:
  secretName: ${secret_name}
  duration: 8760h    # 1 year
  renewBefore: 720h  # 30 days
  privateKey:
    algorithm: RSA
    size: 2048
  usages:
    - server auth
    - client auth
  dnsNames:
    - ${sts}-0.${sts}-svc.${MDB_NAMESPACE}.svc.cluster.local
    - ${sts}-1.${sts}-svc.${MDB_NAMESPACE}.svc.cluster.local
    - "*.${sts}-svc.${MDB_NAMESPACE}.svc.cluster.local"
    - ${sts}-proxy-svc.${MDB_NAMESPACE}.svc.cluster.local
  issuerRef:
    name: ${MDB_TLS_CA_ISSUER}
    kind: ClusterIssuer
EOF
    kubectl wait --for=condition=Ready certificate/"${secret_name}" \
      -n "${MDB_NAMESPACE}" --context "${K8S_CLUSTER_0_CONTEXT_NAME}" --timeout=60s
  done
done

echo "[ok] All mongot TLS certificates created (2 clusters x 2 shards = 4 secrets)"
