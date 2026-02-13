# Wait for MongoDBSearch to reach Running phase
echo "Waiting for MongoDBSearch to reach Running phase..."
kubectl wait --context "${K8S_CTX}" -n "${MDB_NS}" \
  --for=jsonpath='{.status.phase}'=Running \
  mdbs/"${MDB_RESOURCE_NAME}" \
  --timeout=600s

echo "MongoDBSearch is running"
kubectl get --context "${K8S_CTX}" -n "${MDB_NS}" mdbs/"${MDB_RESOURCE_NAME}"
