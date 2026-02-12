# Wait for MongoDB resource to reach Running phase
if [[ "${MDB_DEPLOYMENT_TYPE}" == "sharded" ]]; then
  echo "Waiting for MongoDB sharded cluster to reach Running phase..."
else
  echo "Waiting for MongoDB replica set to reach Running phase..."
fi

kubectl wait --context "${K8S_CTX}" -n "${MDB_NS}" \
  --for=jsonpath='{.status.phase}'=Running \
  mongodb/"${MDB_RESOURCE_NAME}" \
  --timeout=900s

echo "MongoDB resource is running"
kubectl get --context "${K8S_CTX}" -n "${MDB_NS}" mongodb/"${MDB_RESOURCE_NAME}"
