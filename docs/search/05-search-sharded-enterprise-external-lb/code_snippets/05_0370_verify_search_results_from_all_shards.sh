# Verify that search results through mongos contain documents from all shards
# This is the definitive test that mongos is correctly aggregating search results

echo "Verifying search results contain documents from all shards..."

echo "=== Step 1: Getting document count and distribution ==="

# Get total document count and verify data exists
doc_info=$(kubectl exec -n "${MDB_NS}" --context "${K8S_CTX}" \
  mongodb-tools-pod -- env MDB_CONNECTION_STRING="${MDB_CONNECTION_STRING}" /bin/bash -eu -c "$(cat <<'EOF'
mongosh "${MDB_CONNECTION_STRING}" --quiet --eval '
  const totalDocs = db.getSiblingDB("sample_mflix").movies.countDocuments();
  print("TOTAL_DOCS:" + totalDocs);

  // Get sample titles for verification
  const samples = db.getSiblingDB("sample_mflix").movies.find({}, { title: 1 }).limit(5).toArray();
  samples.forEach(s => print("SAMPLE:" + s.title));
'
EOF
)" 2>/dev/null)

total_docs=$(echo "${doc_info}" | grep "^TOTAL_DOCS:" | cut -d: -f2)
echo "Total documents in collection: ${total_docs}"
echo "Sample documents:"
echo "${doc_info}" | grep "^SAMPLE:" | cut -d: -f2 | sed 's/^/  /'

echo ""
echo "=== Step 2: Running search query through mongos ==="

search_results=$(kubectl exec -n "${MDB_NS}" --context "${K8S_CTX}" \
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
    { $project: { _id: 0, title: 1 } }
  ]).toArray();

  print("SEARCH_COUNT:" + results.length);
  results.forEach(r => print("RESULT:" + r.title));
'
EOF
)" 2>/dev/null)

search_count=$(echo "${search_results}" | grep "^SEARCH_COUNT:" | cut -d: -f2)
echo "Search through mongos returned ${search_count} documents"

echo ""
echo "=== Step 3: Verifying search returns all documents ==="

# Verify search count matches total document count
if [[ "${search_count:-0}" -eq "${total_docs:-0}" ]] && [[ "${total_docs:-0}" -gt 0 ]]; then
  echo "✓ Search returned all ${search_count} documents"
  echo ""
  echo "SUCCESS: Search results contain documents from ALL shards"
  echo "Mongos is correctly aggregating search results across the sharded cluster"
else
  echo "✗ Search returned ${search_count:-0} documents but collection has ${total_docs:-0}"
  echo ""
  echo "ERROR: Search results count mismatch - possible shard aggregation issue"
  exit 1
fi
