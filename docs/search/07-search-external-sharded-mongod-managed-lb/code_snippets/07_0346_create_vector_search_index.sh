#!/usr/bin/env bash
# Create a vector search index on the embedded_movies collection
#
# Vector search indexes enable similarity search on vector embeddings.
# The sample_mflix dataset includes pre-computed embeddings in embedded_movies.

echo "Creating vector search index on sample_mflix.embedded_movies..."

user_conn="${MDB_USER_CONNECTION_STRING}"

# shellcheck disable=SC2016,SC1078,SC1079
kubectl exec mongodb-tools -n "${MDB_NS}" --context "${K8S_CTX}" -- mongosh "${user_conn}" --quiet --eval '
  use sample_mflix;

  // Check if collection exists
  const collections = db.getCollectionNames();
  if (!collections.includes("embedded_movies")) {
    print("Warning: embedded_movies collection not found.");
    print("Vector search index creation skipped.");
  } else {
    // Check if index already exists
    const existing = db.embedded_movies.aggregate([
      { $listSearchIndexes: {} }
    ]).toArray();

    if (existing.some(idx => idx.name === "vector_index")) {
      print("Vector search index '\''vector_index'\'' already exists");
    } else {
      // Create vector search index
      db.embedded_movies.createSearchIndex({
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

echo "✓ Vector search index creation initiated"
