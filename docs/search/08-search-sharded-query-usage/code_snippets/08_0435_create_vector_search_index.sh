echo "Creating vector search index" \
  "on sample_mflix.embedded_movies..."

user_conn="${MDB_USER_CONNECTION_STRING:-${MDB_CONNECTION_STRING}}"

kubectl exec mongodb-tools \
  -n "${MDB_NS}" \
  --context "${K8S_CTX}" \
  -- mongosh "${user_conn}" --quiet --eval '
  const db_mflix = db.getSiblingDB("sample_mflix");

  // Check if collection exists
  const collections = db_mflix.getCollectionNames();
  if (!collections.includes("embedded_movies")) {
    print("Warning: embedded_movies collection not found.");
    print("Vector search index creation skipped.");
  } else {
    // Check if index already exists
    const result = db_mflix.runCommand({ listSearchIndexes: "embedded_movies" });
    const existing = (result.ok && result.cursor && result.cursor.firstBatch) ? result.cursor.firstBatch : [];

    if (existing.some(idx => idx.name === "vector_index")) {
      print("Vector search index '\''vector_index'\'' already exists");
    } else {
      // Create vector search index
      db_mflix.embedded_movies.createSearchIndex({
        name: "vector_index",
        type: "vectorSearch",
        definition: {
          fields: [{
            type: "vector",
            path: "plot_embedding",
            numDimensions: 1536,
            similarity: "cosine"
          }]
        }
      });
      print("Vector search index '\''vector_index'\'' created");
    }
  }

  print("\nNote: Vector index may take a few minutes to build and sync.");
'

echo "Vector search index creation initiated"
