# Execute search queries on each shard
# For sharded clusters, search queries are executed directly on each shard

total_results=0

for i in $(seq 0 $((MDB_SHARD_COUNT - 1))); do
  shard_name="${MDB_RESOURCE_NAME}-${i}"
  pod_name="${shard_name}-0"

  echo "=== Executing search query on shard ${i} (pod: ${pod_name}) ==="

  result=$(kubectl exec --context "${K8S_CTX}" -n "${MDB_NS}" "${pod_name}" -- /bin/bash -c '
    /var/lib/mongodb-mms-automation/mongosh-linux-x86_64-*/bin/mongosh \
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
          { \$limit: 5 },
          { \$project: { _id: 0, title: 1, plot: 1, genres: 1, score: { \$meta: \"searchScore\" } } }
        ]).toArray();
        
        print(\"Found \" + results.length + \" results on this shard:\");
        results.forEach(r => printjson(r));
        print(\"COUNT:\" + results.length);
      "
  ' 2>/dev/null)

  echo "${result}"

  count=$(echo "${result}" | grep "^COUNT:" | cut -d: -f2)
  if [[ -n "${count}" ]]; then
    total_results=$((total_results + count))
  fi

  echo ""
done

echo "=== Search Query Summary ==="
echo "Total results across all shards: ${total_results}"

if [[ ${total_results} -gt 0 ]]; then
  echo "SUCCESS: Search queries returned results from sharded cluster"
else
  echo "ERROR: No search results found."
  exit 1
fi

