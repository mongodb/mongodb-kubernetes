#!/usr/bin/env bash
# Import sample data and shard the collection
#
# This imports the sample_mflix dataset and shards the movies collection
# to distribute data across all shards for testing search functionality.
#
# Steps:
# 1. Restore sample_mflix database from MongoDB Atlas sample archive
# 2. Enable sharding on the sample_mflix database
# 3. Shard the movies collection by _id (hashed)
# 4. Wait for chunk distribution across shards

echo "Importing sample_mflix dataset..."

# Connection string for admin operations (through mongos)
# Note: username is "mdb-admin" (not "mdb-admin-user" which is the CRD name)
# authMechanism=SCRAM-SHA-256 is required because MongoDB 8.2+ defaults to SCRAM-SHA-256
admin_conn="mongodb://mdb-admin:${MDB_ADMIN_USER_PASSWORD}@${MDB_EXTERNAL_CLUSTER_NAME}-mongos-0.${MDB_EXTERNAL_CLUSTER_NAME}-svc.${MDB_NS}.svc.cluster.local:27017/?tls=true&tlsCAFile=/tls/ca-pem&authSource=admin&authMechanism=SCRAM-SHA-256"

# Download and restore sample data
kubectl exec mongodb-tools -n "${MDB_NS}" --context "${K8S_CTX}" -- bash -c "
  echo 'Downloading sample_mflix archive...'
  curl -sL 'https://atlas-education.s3.amazonaws.com/sample_mflix.archive' -o /tmp/sample_mflix.archive

  echo 'Restoring database...'
  mongorestore --archive=/tmp/sample_mflix.archive \
    --uri='${admin_conn}' \
    --nsInclude='sample_mflix.*' \
    --drop \
    --numParallelCollections=1 \
    --numInsertionWorkersPerCollection=1

  rm -f /tmp/sample_mflix.archive
  echo 'Database restored.'
"

echo "✓ sample_mflix database restored"

# Enable sharding and shard the movies collection
echo "Configuring sharding..."

kubectl exec mongodb-tools -n "${MDB_NS}" --context "${K8S_CTX}" -- mongosh "${admin_conn}" --quiet --eval '
  // Enable sharding on database
  sh.enableSharding("sample_mflix");
  print("Enabled sharding on sample_mflix database");

  // Create hashed index for sharding
  db.getSiblingDB("sample_mflix").movies.createIndex({ _id: "hashed" });
  print("Created hashed index on movies._id");

  // Shard the collection
  sh.shardCollection("sample_mflix.movies", { _id: "hashed" });
  print("Sharded sample_mflix.movies collection");

  // Wait a moment for balancer
  sleep(5000);

  // Show shard distribution
  print("\nShard distribution:");
  db.getSiblingDB("sample_mflix").movies.getShardDistribution();
'

echo ""
echo "✓ Sample data imported and sharded"
echo ""
echo "Collection 'sample_mflix.movies' is now distributed across all shards."
echo "Each shard's mongot will sync its portion of the data."
