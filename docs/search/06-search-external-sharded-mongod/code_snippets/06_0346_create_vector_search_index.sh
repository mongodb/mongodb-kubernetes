# Create vector search index through mongos
# For sharded clusters, vector search indexes are created through mongos

echo "Creating vector search index through mongos..."

kubectl exec -n "${MDB_NS}" --context "${K8S_CTX}" \
  mongodb-tools-pod -- env MDB_CONNECTION_STRING="${MDB_CONNECTION_STRING}" /bin/bash -eu -c "$(cat <<'EOF'
mongosh "${MDB_CONNECTION_STRING}" --eval '
  print("Connecting to MongoDB and creating vector search index...");
  print("Database: sample_mflix, Collection: embedded_movies");

  try {
    const db_mflix = db.getSiblingDB("sample_mflix");

    // First verify the collection exists
    const collections = db_mflix.getCollectionNames();
    print("Collections in sample_mflix: " + collections.join(", "));

    if (!collections.includes("embedded_movies")) {
      print("ERROR: embedded_movies collection does not exist!");
      quit(1);
    }

    // Check if index already exists
    print("Checking for existing vector search indexes...");
    const existing = db_mflix.runCommand({ listSearchIndexes: "embedded_movies" });
    print("listSearchIndexes result: " + JSON.stringify(existing));

    const hasVectorIndex = existing.ok && existing.cursor && existing.cursor.firstBatch &&
      existing.cursor.firstBatch.some(idx => idx.name === "vector_index");

    if (hasVectorIndex) {
      print("Vector search index already exists");
    } else {
      print("Creating new vector search index...");
      const result = db_mflix.embedded_movies.createSearchIndex("vector_index", "vectorSearch", {
        "fields": [{
          "type": "vector",
          "path": "plot_embedding_voyage_3_large",
          "numDimensions": 2048,
          "similarity": "dotProduct",
          "quantization": "scalar"
        }]
      });
      print("createSearchIndex result: " + JSON.stringify(result));
      print("Vector search index created");
    }
  } catch (e) {
    print("ERROR creating vector search index: " + e.message);
    print("Error stack: " + e.stack);
    quit(1);
  }
'
EOF
)"

echo "Vector search index creation complete"
