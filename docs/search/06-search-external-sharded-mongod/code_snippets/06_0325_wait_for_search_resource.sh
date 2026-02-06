# Wait for MongoDBSearch resource to be ready
#
# This waits for the MongoDBSearch resource to reach the Running state
# and verifies all mongot pods are running

echo "Waiting for MongoDBSearch '${MDB_SEARCH_RESOURCE_NAME}' to be ready..."

# Wait for the MongoDBSearch resource to be in Running state
# Increase timeout to 20 minutes to allow for search index initial sync
kubectl --context "${K8S_CTX}" -n "${MDB_NS}" wait --for=jsonpath='{.status.phase}'=Running \
  mongodbsearch "${MDB_SEARCH_RESOURCE_NAME}" --timeout=1200s

echo "MongoDBSearch '${MDB_SEARCH_RESOURCE_NAME}' is ready"

# Display search resource status
kubectl --context "${K8S_CTX}" -n "${MDB_NS}" get mongodbsearch "${MDB_SEARCH_RESOURCE_NAME}"

# Display mongot pods
echo ""
echo "Mongot pods:"
kubectl --context "${K8S_CTX}" -n "${MDB_NS}" get pods -l search.mongodb.com/mongodbsearch="${MDB_SEARCH_RESOURCE_NAME}"

