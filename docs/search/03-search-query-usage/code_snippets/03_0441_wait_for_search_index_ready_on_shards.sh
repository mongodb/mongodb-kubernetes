# Wait for search indexes to be ready on all shards

max_attempts=60
sleep_time=5

for i in $(seq 0 $((MDB_SHARD_COUNT - 1))); do
  shard_name="${MDB_RESOURCE_NAME}-${i}"
  pod_name="${shard_name}-0"
  
  echo "Checking search index status on shard ${i} (pod: ${pod_name})..."
  
  for attempt in $(seq 1 ${max_attempts}); do
    status=$(kubectl exec --context "${K8S_CTX}" -n "${MDB_NS}" "${pod_name}" -- /bin/bash -c '
      /var/lib/mongodb-mms-automation/mongosh-linux-x86_64-*/bin/mongosh \
        --username mdb-admin \
        --password "'"${MDB_ADMIN_USER_PASSWORD}"'" \
        --authenticationDatabase admin \
        --quiet \
        --eval "
          const result = db.getSiblingDB(\"sample_mflix\").runCommand({ listSearchIndexes: \"movies\" });
          if (result.ok && result.cursor && result.cursor.firstBatch && result.cursor.firstBatch.length > 0) {
            const idx = result.cursor.firstBatch[0];
            print(idx.status || \"READY\");
          } else {
            print(\"NO_INDEX\");
          }
        "
    ' 2>/dev/null | tail -1)

    echo "  Attempt ${attempt}/${max_attempts}: Index status = ${status}"

    if [[ "${status}" == "READY" ]]; then
      echo "  Search index is READY on shard ${i}"
      break
    fi

    if [[ ${attempt} -eq ${max_attempts} ]]; then
      echo "  ERROR: Search index not ready on shard ${i} after ${max_attempts} attempts"
      exit 1
    fi

    sleep ${sleep_time}
  done
done

