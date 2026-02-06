# Create search index through mongos
# For sharded clusters, search indexes are created through mongos which propagates to all shards

echo "Creating search index through mongos..."

kubectl exec -n "${MDB_NS}" --context "${K8S_CTX}" \
  mongodb-tools-pod -- env MDB_CONNECTION_STRING="${MDB_CONNECTION_STRING}" /bin/bash -eu -c "$(cat <<'EOF'
mongosh "${MDB_CONNECTION_STRING}" --eval '
  print("Connecting to MongoDB and creating search index...");
  print("Database: sample_mflix, Collection: movies");

  try {
    // First verify the collection exists
    const collections = db.getSiblingDB("sample_mflix").getCollectionNames();
    print("Collections in sample_mflix: " + collections.join(", "));

    if (!collections.includes("movies")) {
      print("ERROR: movies collection does not exist!");
      quit(1);
    }

    // Check if index already exists
    print("Checking for existing search indexes...");
    const existing = db.getSiblingDB("sample_mflix").runCommand({ listSearchIndexes: "movies" });
    print("listSearchIndexes result: " + JSON.stringify(existing));

    if (existing.ok && existing.cursor && existing.cursor.firstBatch && existing.cursor.firstBatch.length > 0) {
      print("Search index already exists");
    } else {
      print("Creating new search index...");
      const result = db.getSiblingDB("sample_mflix").movies.createSearchIndex(
        "default",
        { mappings: { dynamic: true } }
      );
      print("createSearchIndex result: " + JSON.stringify(result));
      print("Search index created");
    }
  } catch (e) {
    print("ERROR creating search index: " + e.message);
    print("Error stack: " + e.stack);
    quit(1);
  }
'
EOF
)"

echo "Search index creation complete"
