echo "Replicating Search Secrets to the member clusters..."

replicate_secret() {
  local name="$1" dst_ctx="$2"
  kubectl --context "${K8S_CTX_0}" -n "${MDB_NS}" get secret "${name}" -o json \
    | jq 'del(.metadata.resourceVersion, .metadata.uid, .metadata.creationTimestamp, .metadata.namespace, .metadata.ownerReferences, .metadata.managedFields, .status)' \
    | kubectl --context "${dst_ctx}" -n "${MDB_NS}" apply -f -
}

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
