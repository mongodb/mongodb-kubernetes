# Create search index on each shard directly
# For sharded clusters, search indexes must be created on each shard individually

for i in $(seq 0 $((MDB_SHARD_COUNT - 1))); do
  shard_name="${MDB_RESOURCE_NAME}-${i}"
  pod_name="${shard_name}-0"
  
  echo "Creating search index on shard ${i} (pod: ${pod_name})..."
  
  kubectl exec --context "${K8S_CTX}" -n "${MDB_NS}" "${pod_name}" -- /bin/bash -c '
    /var/lib/mongodb-mms-automation/mongosh-linux-x86_64-*/bin/mongosh \
      --username mdb-admin \
      --password "'"${MDB_ADMIN_USER_PASSWORD}"'" \
      --authenticationDatabase admin \
      --quiet \
      --eval "
        db.getSiblingDB(\"sample_mflix\").movies.createSearchIndex(
          \"default\",
          { mappings: { dynamic: true } }
        );
        print(\"Search index created on shard '"${i}"'\");
      "
  '
done

