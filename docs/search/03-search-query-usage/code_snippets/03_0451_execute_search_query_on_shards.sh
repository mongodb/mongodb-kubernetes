# Execute search queries on each shard directly
# For sharded clusters with per-shard mongot, search queries must be executed on each shard
# because mongos doesn't have mongotHost configured

# Determine TLS options based on whether TLS is enabled
TLS_OPTIONS=""
if [[ "${MDB_TLS_ENABLED:-false}" == "true" ]]; then
  TLS_OPTIONS="--tls --tlsAllowInvalidCertificates"
fi

echo "=== Executing search query on each shard ==="

total_results=0

for i in $(seq 0 $((MDB_SHARD_COUNT - 1))); do
  shard_name="${MDB_RESOURCE_NAME}-${i}"
  pod_name="${shard_name}-0"
  echo ""
  echo "=== Shard ${i} (${pod_name}) ==="

  # shellcheck disable=SC2016
  result=$(kubectl exec --context "${K8S_CTX}" -n "${MDB_NS}" "${pod_name}" -- /bin/bash -c '
    /var/lib/mongodb-mms-automation/mongosh-linux-x86_64-*/bin/mongosh \
      '"${TLS_OPTIONS}"' \
      --username mdb-admin \
      --password "'"${MDB_ADMIN_USER_PASSWORD}"'" \
      --authenticationDatabase admin \
      --quiet \
      --eval "
        const results = db.getSiblingDB(\"sample_mflix\").movies.aggregate([
          {
            \$search: {
              index: \"default\",
              text: {
                query: \"matrix OR space OR dream\",
                path: { wildcard: \"*\" }
              }
            }
          },
          { \$limit: 10 },
          { \$project: { _id: 0, title: 1, plot: 1, genres: 1, score: { \$meta: \"searchScore\" } } }
        ]).toArray();

        print(\"Found \" + results.length + \" results on this shard:\");
        results.forEach(r => printjson(r));
        print(\"\");
        print(\"COUNT:\" + results.length);
      "
  ' 2>&1)

  echo "${result}"

  # Extract count from result
  count=$(echo "${result}" | grep "^COUNT:" | cut -d: -f2 || echo "0")
  total_results=$((total_results + count))
done

echo ""
echo "=== Search Query Summary ==="
echo "Total results across all shards: ${total_results}"
echo "Search queries executed on all shards successfully"
