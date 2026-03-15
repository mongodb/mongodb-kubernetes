#!/usr/bin/env bash
# Import sample data and shard the collection
#
# This imports the sample_mflix dataset and shards the movies collection
# to distribute data across all shards for testing search functionality.
#
# Steps:
# 1. Download sample_mflix archive from MongoDB Atlas sample data
# 2. Restore the database using mongorestore
# 3. Enable sharding on the sample_mflix database
# 4. Shard the movies collection by _id (hashed)
# 5. Verify chunk distribution across shards
#
# ============================================================================
# DEPENDS ON: 07_0335_run_mongodb_tools_pod.sh (mongodb-tools pod must exist)
# ============================================================================

echo "Importing sample_mflix dataset..."
echo ""

# ============================================================================
# CONNECTION STRING
# ============================================================================
# Note: The MongoDB username is "mdb-admin" (the actual username)
#       NOT "mdb-admin-user" (which is the MongoDBUser CRD name)
#
# authMechanism=SCRAM-SHA-256 is required because MongoDB 8.2+ only enables
# SCRAM-SHA-256 by default (not the older SCRAM-SHA-1)
# ============================================================================
admin_conn="mongodb://mdb-admin:${MDB_ADMIN_USER_PASSWORD}@${MDB_EXTERNAL_CLUSTER_NAME}-mongos-0.${MDB_EXTERNAL_CLUSTER_NAME}-svc.${MDB_NS}.svc.cluster.local:27017/?tls=true&tlsCAFile=/tls/ca-pem&authSource=admin&authMechanism=SCRAM-SHA-256"

# ============================================================================
# STEP 1: Download sample data archive
# ============================================================================
# The archive is ~50MB and contains the sample_mflix database with movies,
# comments, users, theaters, and embedded_movies collections.
# ============================================================================
echo "Step 1: Downloading sample_mflix archive (~50MB)..."
kubectl exec mongodb-tools -n "${MDB_NS}" --context "${K8S_CTX}" -- \
  curl -sL 'https://atlas-education.s3.amazonaws.com/sample_mflix.archive' \
    -o /tmp/sample_mflix.archive

echo "  ✓ Download complete"

# ============================================================================
# STEP 2: Restore database using mongorestore
# ============================================================================
# --drop: Remove existing collections before restore (idempotent)
# --nsInclude: Only restore sample_mflix.* collections
# --numParallelCollections=1: Reduce memory usage
# ============================================================================
echo ""
echo "Step 2: Restoring database..."
kubectl exec mongodb-tools -n "${MDB_NS}" --context "${K8S_CTX}" -- \
  mongorestore --archive=/tmp/sample_mflix.archive \
    --uri="${admin_conn}" \
    --nsInclude='sample_mflix.*' \
    --drop \
    --numParallelCollections=1 \
    --numInsertionWorkersPerCollection=1

echo "  ✓ Database restored"

# ============================================================================
# STEP 3: Clean up temporary file
# ============================================================================
echo ""
echo "Step 3: Cleaning up temporary file..."
kubectl exec mongodb-tools -n "${MDB_NS}" --context "${K8S_CTX}" -- \
  rm -f /tmp/sample_mflix.archive

echo "  ✓ Cleanup complete"

# ============================================================================
# STEP 4: Enable sharding and shard the movies collection
# ============================================================================
# sh.enableSharding(): Marks database as sharding-enabled
# createIndex({ _id: "hashed" }): Creates the index needed for hashed sharding
# sh.shardCollection(): Distributes the collection across shards
# ============================================================================
echo ""
echo "Step 4: Configuring sharding..."

# Enable sharding on the database
kubectl exec mongodb-tools -n "${MDB_NS}" --context "${K8S_CTX}" -- \
  mongosh "${admin_conn}" --quiet --eval 'sh.enableSharding("sample_mflix")'
echo "  ✓ Enabled sharding on sample_mflix database"

# Create hashed index for sharding (required before shardCollection)
kubectl exec mongodb-tools -n "${MDB_NS}" --context "${K8S_CTX}" -- \
  mongosh "${admin_conn}" --quiet --eval \
    'db.getSiblingDB("sample_mflix").movies.createIndex({ _id: "hashed" })'
echo "  ✓ Created hashed index on movies._id"

# Shard the collection using the hashed index
kubectl exec mongodb-tools -n "${MDB_NS}" --context "${K8S_CTX}" -- \
  mongosh "${admin_conn}" --quiet --eval \
    'sh.shardCollection("sample_mflix.movies", { _id: "hashed" })'
echo "  ✓ Sharded sample_mflix.movies collection"

# ============================================================================
# STEP 5: Verify shard distribution
# ============================================================================
echo ""
echo "Step 5: Verifying shard distribution..."
echo "  (Waiting 5 seconds for balancer...)"
sleep 5

kubectl exec mongodb-tools -n "${MDB_NS}" --context "${K8S_CTX}" -- \
  mongosh "${admin_conn}" --quiet --eval \
    'db.getSiblingDB("sample_mflix").movies.getShardDistribution()'

echo ""
echo "✓ Sample data imported and sharded"
echo ""
echo "Collection 'sample_mflix.movies' is now distributed across all shards."
echo "Each shard's mongot will sync its portion of the data."
