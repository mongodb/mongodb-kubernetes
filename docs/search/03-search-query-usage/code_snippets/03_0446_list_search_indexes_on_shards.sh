# List search indexes on each shard
# For sharded clusters with per-shard mongot, we list indexes from each shard directly

# Determine TLS options based on whether TLS is enabled
TLS_OPTIONS=""
if [[ "${MDB_TLS_ENABLED:-false}" == "true" ]]; then
  TLS_OPTIONS="--tls --tlsAllowInvalidCertificates"
fi

echo "Listing search indexes on all shards..."

for i in $(seq 0 $((MDB_SHARD_COUNT - 1))); do
  shard_name="${MDB_RESOURCE_NAME}-${i}"
  pod_name="${shard_name}-0"
  echo "=== Shard ${i} (${pod_name}) ==="

  kubectl exec --context "${K8S_CTX}" -n "${MDB_NS}" "${pod_name}" -- /bin/bash -c '
    /var/lib/mongodb-mms-automation/mongosh-linux-x86_64-*/bin/mongosh \
      '"${TLS_OPTIONS}"' \
      --username mdb-admin \
      --password "'"${MDB_ADMIN_USER_PASSWORD}"'" \
      --authenticationDatabase admin \
      --quiet \
      --eval "
        db.getSiblingDB(\"sample_mflix\").movies.getSearchIndexes().forEach(idx => printjson(idx));
      "
  ' || echo "Failed to list search indexes on shard ${i}"
done
