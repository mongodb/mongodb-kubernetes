#!/usr/bin/env bash
# Import sample_mflix dataset, shard the movies collection

echo "Importing sample_mflix dataset..."

admin_conn="mongodb://mdb-admin:${MDB_ADMIN_USER_PASSWORD}@${MDB_EXTERNAL_MONGOS_NAME}-0.${MDB_EXTERNAL_MONGOS_SVC}.${MDB_NS}.svc.cluster.local:27017/?tls=true&tlsCAFile=/tls/ca-pem&authSource=admin&authMechanism=SCRAM-SHA-256"

echo "Downloading sample_mflix archive (~50MB)..."
kubectl exec mongodb-tools -n "${MDB_NS}" --context "${K8S_CTX}" -- \
  curl -sL 'https://atlas-education.s3.amazonaws.com/sample_mflix.archive' \
    -o /tmp/sample_mflix.archive
echo "  ✓ Download complete"

echo "Restoring database..."
kubectl exec mongodb-tools -n "${MDB_NS}" --context "${K8S_CTX}" -- \
  mongorestore --archive=/tmp/sample_mflix.archive \
    --uri="${admin_conn}" \
    --nsInclude='sample_mflix.*' \
    --drop \
    --numParallelCollections=1 \
    --numInsertionWorkersPerCollection=1
echo "  ✓ Database restored"

kubectl exec mongodb-tools -n "${MDB_NS}" --context "${K8S_CTX}" -- \
  rm -f /tmp/sample_mflix.archive

echo "Configuring sharding..."

kubectl exec mongodb-tools -n "${MDB_NS}" --context "${K8S_CTX}" -- \
  mongosh "${admin_conn}" --quiet --eval 'sh.enableSharding("sample_mflix")'
echo "  ✓ Enabled sharding on sample_mflix database"

kubectl exec mongodb-tools -n "${MDB_NS}" --context "${K8S_CTX}" -- \
  mongosh "${admin_conn}" --quiet --eval \
    'db.getSiblingDB("sample_mflix").movies.createIndex({ _id: "hashed" })'
echo "  ✓ Created hashed index on movies._id"

kubectl exec mongodb-tools -n "${MDB_NS}" --context "${K8S_CTX}" -- \
  mongosh "${admin_conn}" --quiet --eval \
    'sh.shardCollection("sample_mflix.movies", { _id: "hashed" })'
echo "  ✓ Sharded sample_mflix.movies collection"

echo "Verifying shard distribution..."
sleep 5

kubectl exec mongodb-tools -n "${MDB_NS}" --context "${K8S_CTX}" -- \
  mongosh "${admin_conn}" --quiet --eval \
    'db.getSiblingDB("sample_mflix").movies.getShardDistribution()'

echo ""
echo "✓ Sample data imported and sharded"
