# Wait for search index to be ready on all shards
# For sharded clusters with per-shard mongot, we check each shard directly

# Determine TLS options based on whether TLS is enabled
TLS_OPTIONS=""
if [[ "${MDB_TLS_ENABLED:-false}" == "true" ]]; then
  TLS_OPTIONS="--tls --tlsAllowInvalidCertificates"
fi

echo "Waiting for search index to be ready on all shards..."

max_attempts=60
sleep_time=5

for attempt in $(seq 1 ${max_attempts}); do
  all_ready=true

  for i in $(seq 0 $((MDB_SHARD_COUNT - 1))); do
    shard_name="${MDB_RESOURCE_NAME}-${i}"
    pod_name="${shard_name}-0"

    status=$(kubectl exec --context "${K8S_CTX}" -n "${MDB_NS}" "${pod_name}" -- /bin/bash -c '
      /var/lib/mongodb-mms-automation/mongosh-linux-x86_64-*/bin/mongosh \
        '"${TLS_OPTIONS}"' \
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
    ' 2>/dev/null | grep -v "^Warning:" | grep -v "^Defaulted container" | tail -1)

    echo "Attempt ${attempt}/${max_attempts}: Shard ${i} index status = ${status}"

    if [[ "${status}" != "READY" ]]; then
      all_ready=false
    fi
  done

  if [[ "${all_ready}" == "true" ]]; then
    echo "Search index is READY on all shards"
    exit 0
  fi

  if [[ ${attempt} -eq ${max_attempts} ]]; then
    echo "ERROR: Search index not ready on all shards after ${max_attempts} attempts"
    exit 1
  fi

  sleep ${sleep_time}
done
