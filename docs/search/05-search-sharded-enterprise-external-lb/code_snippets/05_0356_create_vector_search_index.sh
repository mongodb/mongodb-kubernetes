# Create vector search index through mongos
# For sharded clusters, vector search indexes are created through mongos

echo "Creating vector search index through mongos..."

kubectl exec -n "${MDB_NS}" --context "${K8S_CTX}" \
  mongodb-tools-pod -- env MDB_CONNECTION_STRING="${MDB_CONNECTION_STRING}" /bin/bash -eu -c "$(cat <<'EOF'
mongosh "${MDB_CONNECTION_STRING}" --quiet --eval '
  const db_mflix = db.getSiblingDB("sample_mflix");

  // Check if index already exists
  const existing = db_mflix.runCommand({ listSearchIndexes: "embedded_movies" });
  const hasVectorIndex = existing.ok && existing.cursor && existing.cursor.firstBatch &&
    existing.cursor.firstBatch.some(idx => idx.name === "vector_index");

  if (hasVectorIndex) {
    print("Vector search index already exists");
  } else {
    db_mflix.embedded_movies.createSearchIndex("vector_index", "vectorSearch", {
      "fields": [{
        "type": "vector",
        "path": "plot_embedding_voyage_3_large",
        "numDimensions": 2048,
        "similarity": "dotProduct",
        "quantization": "scalar"
      }]
    });
    print("Vector search index created");
  }
'
EOF
)"

echo "Vector search index creation complete"
