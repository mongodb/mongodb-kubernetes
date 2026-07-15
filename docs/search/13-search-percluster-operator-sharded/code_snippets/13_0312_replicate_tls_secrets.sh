echo "Replicating cluster-1 TLS secrets from cluster 0 (where cert-manager minted them)..."

# cert-manager only ran on cluster 0, so every secret named for cluster index 1
# was actually created THERE. Copy those (and only those -- cluster 0's own
# secrets are already in place) into cluster 1. This is the operator-per-cluster
# tradeoff called out in the README: nothing replicates secrets for you.
cert_pfx="${MDBS_TLS_CERT_SECRET_PREFIX}-${MDBS_RESOURCE_NAME}"

copy_secret_to_cluster1() {
  local secret_name="$1"
  kubectl get secret "${secret_name}" \
    -n "${MDB_NAMESPACE}" --context "${K8S_CLUSTER_0_CONTEXT_NAME}" -o json \
    | jq 'del(.metadata.resourceVersion, .metadata.uid, .metadata.creationTimestamp, .metadata.ownerReferences, .metadata.annotations, .metadata.managedFields, .metadata.selfLink)' \
    | kubectl apply --context "${K8S_CLUSTER_1_CONTEXT_NAME}" -n "${MDB_NAMESPACE}" -f -
  echo "  [ok] ${secret_name} copied to cluster ${K8S_CLUSTER_1_CONTEXT_NAME}"
}

for shard_name in "${MDB_SHARD_0_NAME}" "${MDB_SHARD_1_NAME}"; do
  copy_secret_to_cluster1 "${cert_pfx}-search-${MDBS_CLUSTER_1_INDEX}-${shard_name}-cert"
done
copy_secret_to_cluster1 "${cert_pfx}-search-lb-${MDBS_CLUSTER_1_INDEX}-cert"
copy_secret_to_cluster1 "${cert_pfx}-search-lb-${MDBS_CLUSTER_1_INDEX}-client-cert"

echo "[ok] Cluster-1 TLS secrets replicated"
