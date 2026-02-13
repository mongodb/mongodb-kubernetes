# Restore sample_mflix database to sharded cluster through mongos.
# Then shard the movies and embedded_movies collections to distribute data across all shards.
# Provide any TLS parameters directly within MDB_CONNECTION_STRING.

# Build admin connection string (replace mdb-user with mdb-admin)
ADMIN_CONNECTION_STRING="${MDB_CONNECTION_STRING/mdb-user:${MDB_USER_PASSWORD}/mdb-admin:${MDB_ADMIN_USER_PASSWORD}}"

kubectl exec -n "${MDB_NS}" --context "${K8S_CTX}" \
  mongodb-tools-pod -- env MDB_CONNECTION_STRING="${MDB_CONNECTION_STRING}" ADMIN_CONNECTION_STRING="${ADMIN_CONNECTION_STRING}" /bin/bash -eu -c "$(cat <<'EOF'
echo "Downloading sample database archive..."
curl -fSL https://atlas-education.s3.amazonaws.com/sample_mflix.archive -o /tmp/sample_mflix.archive

echo "Restoring sample database through mongos"
mongorestore \
  --archive=/tmp/sample_mflix.archive \
  --verbose=1 \
  --drop \
  --nsInclude 'sample_mflix.*' \
  --uri="${MDB_CONNECTION_STRING}"

echo ""
echo "Sharding the collections to distribute data across all shards..."
mongosh "${ADMIN_CONNECTION_STRING}" --quiet --eval '
  // Enable sharding on the database
  print("Enabling sharding on sample_mflix database...");
  sh.enableSharding("sample_mflix");

  // Shard the movies collection (for text search)
  print("Sharding movies collection...");
  db.getSiblingDB("sample_mflix").movies.createIndex({ _id: "hashed" });
  sh.shardCollection("sample_mflix.movies", { _id: "hashed" });

  // Shard the embedded_movies collection (for vector search)
  print("Sharding embedded_movies collection...");
  db.getSiblingDB("sample_mflix").embedded_movies.createIndex({ _id: "hashed" });
  sh.shardCollection("sample_mflix.embedded_movies", { _id: "hashed" });

  print("Collections sharded successfully");

  // Wait for balancer to distribute chunks
  print("Waiting for chunk distribution...");
  sleep(10000);

  // Show shard distribution
  print("");
  print("=== movies collection shard distribution ===");
  const moviesStats = db.getSiblingDB("sample_mflix").movies.stats();
  if (moviesStats.shards) {
    Object.keys(moviesStats.shards).forEach(shard => {
      const s = moviesStats.shards[shard];
      const pct = ((s.count / moviesStats.count) * 100).toFixed(1);
      print("  " + shard + ": " + s.count + " docs (" + pct + "%)");
    });
  }

  print("");
  print("=== embedded_movies collection shard distribution ===");
  const embeddedStats = db.getSiblingDB("sample_mflix").embedded_movies.stats();
  if (embeddedStats.shards) {
    Object.keys(embeddedStats.shards).forEach(shard => {
      const s = embeddedStats.shards[shard];
      const pct = ((s.count / embeddedStats.count) * 100).toFixed(1);
      print("  " + shard + ": " + s.count + " docs (" + pct + "%)");
    });
  }
'
EOF
)"

echo "Data import and sharding complete"

