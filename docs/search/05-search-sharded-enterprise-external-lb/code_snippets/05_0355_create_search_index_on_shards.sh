# Create search index through mongos
# For sharded clusters, search indexes are created through mongos which propagates to all shards

echo "Creating search index through mongos..."

kubectl exec -n "${MDB_NS}" --context "${K8S_CTX}" \
  mongodb-tools-pod -- env MDB_CONNECTION_STRING="${MDB_CONNECTION_STRING}" /bin/bash -eu -c "$(cat <<'EOF'
mongosh "${MDB_CONNECTION_STRING}" --quiet --eval '
  // Check if index already exists
  const existing = db.getSiblingDB("sample_mflix").runCommand({ listSearchIndexes: "movies" });
  if (existing.ok && existing.cursor && existing.cursor.firstBatch && existing.cursor.firstBatch.length > 0) {
    print("Search index already exists");
  } else {
    db.getSiblingDB("sample_mflix").movies.createSearchIndex(
      "default",
      { mappings: { dynamic: true } }
    );
    print("Search index created");
  }
'
EOF
)"

echo "Search index creation complete"
