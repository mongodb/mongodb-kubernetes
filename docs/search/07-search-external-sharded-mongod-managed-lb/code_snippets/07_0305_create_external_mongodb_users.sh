#!/usr/bin/env bash
# Create user credential secrets for the simulated external MongoDB cluster
#
# These secrets store passwords for MongoDB users. In a real external cluster
# scenario, you would create these users directly on your external MongoDB.
#
# Users created:
# - mdb-admin-user: Cluster administrator
# - mdb-user: Application user for running search queries
# - search-sync-source: User for mongot to sync data from MongoDB

echo "Creating MongoDB user credential secrets..."

# Admin user password secret
kubectl create secret generic "${MDB_EXTERNAL_CLUSTER_NAME}-mdb-admin-user" \
  --from-literal=password="${MDB_ADMIN_USER_PASSWORD}" \
  -n "${MDB_NS}" \
  --context "${K8S_CTX}" \
  --dry-run=client -o yaml | kubectl apply --context "${K8S_CTX}" -f -
echo "  ✓ Admin user secret created"

# Application user password secret
kubectl create secret generic "${MDB_EXTERNAL_CLUSTER_NAME}-mdb-user" \
  --from-literal=password="${MDB_USER_PASSWORD}" \
  -n "${MDB_NS}" \
  --context "${K8S_CTX}" \
  --dry-run=client -o yaml | kubectl apply --context "${K8S_CTX}" -f -
echo "  ✓ Application user secret created"

# Search sync source user password secret
# This user is used by mongot to sync data from MongoDB
kubectl create secret generic "${MDB_SEARCH_RESOURCE_NAME}-search-sync-source-password" \
  --from-literal=password="${MDB_SEARCH_SYNC_USER_PASSWORD}" \
  -n "${MDB_NS}" \
  --context "${K8S_CTX}" \
  --dry-run=client -o yaml | kubectl apply --context "${K8S_CTX}" -f -
echo "  ✓ Search sync source user secret created"

echo "✓ All MongoDB user secrets created"

