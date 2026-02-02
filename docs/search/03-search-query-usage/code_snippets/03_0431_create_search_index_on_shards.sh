# Create search index directly on each shard
# For sharded clusters with per-shard mongot, search indexes must be created on each shard
# because mongos doesn't have searchIndexManagementHostAndPort configured

# Determine TLS options based on whether TLS is enabled
TLS_OPTIONS=""
if [[ "${MDB_TLS_ENABLED:-false}" == "true" ]]; then
  TLS_OPTIONS="--tls --tlsAllowInvalidCertificates"
fi

echo "Creating search index on each shard..."

for i in $(seq 0 $((MDB_SHARD_COUNT - 1))); do
  shard_name="${MDB_RESOURCE_NAME}-${i}"
  pod_name="${shard_name}-0"
  echo "Creating search index on shard ${i} (pod: ${pod_name})..."

  kubectl exec --context "${K8S_CTX}" -n "${MDB_NS}" "${pod_name}" -- /bin/bash -c '
    /var/lib/mongodb-mms-automation/mongosh-linux-x86_64-*/bin/mongosh \
      '"${TLS_OPTIONS}"' \
      --username mdb-admin \
      --password "'"${MDB_ADMIN_USER_PASSWORD}"'" \
      --authenticationDatabase admin \
      --quiet \
      --eval "
        // Check if index already exists
        const existing = db.getSiblingDB(\"sample_mflix\").runCommand({ listSearchIndexes: \"movies\" });
        if (existing.ok && existing.cursor && existing.cursor.firstBatch && existing.cursor.firstBatch.length > 0) {
          print(\"Search index already exists on this shard\");
        } else {
          db.getSiblingDB(\"sample_mflix\").movies.createSearchIndex(
            \"default\",
            { mappings: { dynamic: true } }
          );
          print(\"Search index created on this shard\");
        }
      "
  ' || echo "Failed to create search index on shard ${i}"
done

echo "Search index creation completed on all shards"
