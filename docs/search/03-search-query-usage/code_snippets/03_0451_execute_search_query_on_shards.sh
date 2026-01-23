# Execute search queries through mongos
# For sharded clusters, search queries go through mongos which aggregates results from all shards

echo "=== Executing search query through mongos ==="

kubectl exec -n "${MDB_NS}" --context "${K8S_CTX}" \
  mongodb-tools-pod -- env MDB_CONNECTION_STRING="${MDB_CONNECTION_STRING}" /bin/bash -eu -c "$(cat <<'EOF'
mongosh "${MDB_CONNECTION_STRING}" --quiet --eval '
  const results = db.getSiblingDB("sample_mflix").movies.aggregate([
    {
      $search: {
        index: "default",
        text: {
          query: "matrix OR space OR dream",
          path: { wildcard: "*" }
        }
      }
    },
    { $limit: 10 },
    { $project: { _id: 0, title: 1, plot: 1, genres: 1, score: { $meta: "searchScore" } } }
  ]).toArray();

  print("Found " + results.length + " results:");
  results.forEach(r => printjson(r));
  print("");
  print("COUNT:" + results.length);
'
EOF
)"

echo ""
echo "=== Search Query Summary ==="
echo "Search query executed through mongos successfully"
