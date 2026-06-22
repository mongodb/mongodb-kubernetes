echo "Replicating Search Secrets to the member clusters..."

# The operator auto-replicates the SOURCE certs to member clusters, but NOT
# the Search-prefixed Secrets. Copy the per-(cluster,shard) mongot certs, the
# LB server/client certs, and the search sync user password from the central
# cluster to each additional member cluster, so mongot pods there can mount
# their TLS material (otherwise they stay PodInitializing).
replicate_secret() {
  local name="$1" dst_ctx="$2"
  kubectl --context "${K8S_CTX_0}" -n "${MDB_NS}" get secret "${name}" -o json \
    | jq 'del(.metadata.resourceVersion, .metadata.uid, .metadata.creationTimestamp, .metadata.namespace, .metadata.ownerReferences, .metadata.managedFields, .status)' \
    | kubectl --context "${dst_ctx}" -n "${MDB_NS}" apply -f -
}

# Replicate to every member cluster other than the central one (cluster 0).
dst_clusters=("${K8S_CTX_1}")
for dst in "${dst_clusters[@]}"; do
  for ci in 0 1; do
    for shard_name in "${MDB_SHARD_0_NAME}" "${MDB_SHARD_1_NAME}" "${MDB_SHARD_2_NAME}"; do
      replicate_secret "${MDB_TLS_CERT_SECRET_PREFIX}-${MDB_SEARCH_RESOURCE_NAME}-search-${ci}-${shard_name}-cert" "${dst}"
    done
  done
  replicate_secret "${MDB_TLS_CERT_SECRET_PREFIX}-${MDB_SEARCH_RESOURCE_NAME}-search-lb-0-cert" "${dst}"
  replicate_secret "${MDB_TLS_CERT_SECRET_PREFIX}-${MDB_SEARCH_RESOURCE_NAME}-search-lb-0-client-cert" "${dst}"
  replicate_secret "${MDB_RESOURCE_NAME}-search-sync-source-password" "${dst}"
done

echo "[ok] Search Secrets replicated to member clusters"
