# List search indexes on each shard

for i in $(seq 0 $((MDB_SHARD_COUNT - 1))); do
  shard_name="${MDB_RESOURCE_NAME}-${i}"
  pod_name="${shard_name}-0"
  
  echo "--- Shard ${i} (${pod_name}) ---"
  
  kubectl exec --context "${K8S_CTX}" -n "${MDB_NS}" "${pod_name}" -- /bin/bash -c '
    /var/lib/mongodb-mms-automation/mongosh-linux-x86_64-*/bin/mongosh \
      --username mdb-admin \
      --password "'"${MDB_ADMIN_USER_PASSWORD}"'" \
      --authenticationDatabase admin \
      --quiet \
      --eval "
        db.getSiblingDB(\"sample_mflix\").movies.getSearchIndexes().forEach(idx => printjson(idx));
      "
  '
done

