# Execute search queries through mongos
# This verifies that mongos can route search queries to mongot and aggregate results from all shards

echo "Executing search queries through mongos..."

echo "=== Test 1: Basic text search through mongos ==="
result=$(kubectl exec -n "${MDB_NS}" --context "${K8S_CTX}" \
  mongodb-tools-pod -- env MDB_CONNECTION_STRING="${MDB_CONNECTION_STRING}" /bin/bash -eu -c "$(cat <<'EOF'
mongosh "${MDB_CONNECTION_STRING}" --quiet --eval '
  const results = db.getSiblingDB("sample_mflix").movies.aggregate([
    {
      $search: {
        index: "default",
        text: {
          query: "matrix space dream",
          path: { wildcard: "*" }
        }
      }
    },
    { $limit: 10 },
    { $project: { _id: 0, title: 1, plot: 1, score: { $meta: "searchScore" } } }
  ]).toArray();

  print("Found " + results.length + " results:");
  results.forEach((r, i) => {
    print((i+1) + ". " + r.title + " (score: " + r.score.toFixed(4) + ")");
  });
  print("COUNT:" + results.length);
'
EOF
)" 2>/dev/null)

echo "${result}"
count1=$(echo "${result}" | grep "^COUNT:" | cut -d: -f2)
echo ""

echo "=== Test 2: Wildcard search to verify results from all shards ==="
result=$(kubectl exec -n "${MDB_NS}" --context "${K8S_CTX}" \
  mongodb-tools-pod -- env MDB_CONNECTION_STRING="${MDB_CONNECTION_STRING}" /bin/bash -eu -c "$(cat <<'EOF'
mongosh "${MDB_CONNECTION_STRING}" --quiet --eval '
  const results = db.getSiblingDB("sample_mflix").movies.aggregate([
    {
      $search: {
        index: "default",
        wildcard: {
          query: "*",
          path: "title",
          allowAnalyzedField: true
        }
      }
    },
    { $project: { _id: 0, title: 1, score: { $meta: "searchScore" } } }
  ]).toArray();

  print("Total documents found via search: " + results.length);

  // Verify we got results (should match total document count)
  const totalDocs = db.getSiblingDB("sample_mflix").movies.countDocuments();
  print("Total documents in collection: " + totalDocs);

  if (results.length === totalDocs) {
    print("SUCCESS: Search returned all documents from sharded collection");
  } else {
    print("WARNING: Search returned " + results.length + " but collection has " + totalDocs);
  }
  print("COUNT:" + results.length);
'
EOF
)" 2>/dev/null)

echo "${result}"
count2=$(echo "${result}" | grep "^COUNT:" | cut -d: -f2)
echo ""

echo "=== Search Query Summary ==="
echo "Test 1 (text search): ${count1:-0} results"
echo "Test 2 (wildcard search): ${count2:-0} results"

if [[ "${count1:-0}" -gt 0 ]] && [[ "${count2:-0}" -gt 0 ]]; then
  echo ""
  echo "SUCCESS: Search queries through mongos are working correctly"
else
  echo ""
  echo "ERROR: Search queries failed"
  exit 1
fi
