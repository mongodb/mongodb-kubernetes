# Restore sample_mflix database to sharded cluster through mongos.
# Then shard the movies collection to distribute data across all shards.
# Provide any TLS parameters directly within MDB_CONNECTION_STRING.

# Build admin connection string (replace mdb-user with mdb-admin)
ADMIN_CONNECTION_STRING="${MDB_CONNECTION_STRING//mdb-user:${MDB_USER_PASSWORD}/mdb-admin:${MDB_ADMIN_USER_PASSWORD}}"

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
echo "Sharding the movies collection to distribute data across all shards..."
mongosh "${ADMIN_CONNECTION_STRING}" --quiet --eval '
  // Enable sharding on the database
  sh.enableSharding("sample_mflix");

  // Create a hashed index on _id for sharding
  db.getSiblingDB("sample_mflix").movies.createIndex({ _id: "hashed" });

  // Shard the collection using hashed sharding on _id
  sh.shardCollection("sample_mflix.movies", { _id: "hashed" });

  print("Collection sharded successfully");

  // Wait a moment for balancer to distribute chunks
  print("Waiting for chunk distribution...");
  sleep(5000);

  // Show shard distribution
  const stats = db.getSiblingDB("sample_mflix").movies.getShardDistribution();
'
EOF
)"

echo "Data import and sharding complete"
