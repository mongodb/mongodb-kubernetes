echo "Replicating Search Secrets to the member clusters..."

# The operator auto-replicates the SOURCE cert to member clusters, but NOT the
# Search-prefixed Secrets. Copy the mongot cert, the LB server/client certs,
# and the search sync user password from the central cluster to each
# additional member cluster, so mongot pods there can mount their TLS material
# (otherwise they stay PodInitializing).
replicate_secret() {
  local name="$1" dst_ctx="$2"
  kubectl --context "${K8S_CTX_0}" -n "${MDB_NS}" get secret "${name}" -o json \
    | jq 'del(.metadata.resourceVersion, .metadata.uid, .metadata.creationTimestamp, .metadata.namespace, .metadata.ownerReferences, .metadata.managedFields, .status)' \
    | kubectl --context "${dst_ctx}" -n "${MDB_NS}" apply -f -
}

# Replicate to every member cluster other than the central one (cluster 0).
# The LB server/client certs are per cluster index: each member cluster's Envoy
# mounts search-lb-<index>-cert / -client-cert, so replicate the matching index
# to it (cluster K8S_CTX_1 is index 1). The mongot -search-cert is shared across
# clusters. The central cluster (index 0) keeps its own -search-lb-0- certs.
dst_clusters=("${K8S_CTX_1}")
dst_indexes=(1)
for n in "${!dst_clusters[@]}"; do
  dst="${dst_clusters[${n}]}"
  idx="${dst_indexes[${n}]}"
  replicate_secret "${MDB_TLS_CERT_SECRET_PREFIX}-${MDB_SEARCH_RESOURCE_NAME}-search-cert" "${dst}"
  replicate_secret "${MDB_TLS_CERT_SECRET_PREFIX}-${MDB_SEARCH_RESOURCE_NAME}-search-lb-${idx}-cert" "${dst}"
  replicate_secret "${MDB_TLS_CERT_SECRET_PREFIX}-${MDB_SEARCH_RESOURCE_NAME}-search-lb-${idx}-client-cert" "${dst}"
  replicate_secret "${MDB_RESOURCE_NAME}-search-sync-source-password" "${dst}"
done

echo "[ok] Search Secrets replicated to member clusters"
