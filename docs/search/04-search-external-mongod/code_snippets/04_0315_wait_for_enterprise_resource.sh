echo "Waiting for MongoDB Enterprise resource to reach Running phase..."
kubectl --context "${K8S_CTX}" -n "${MDB_NS}" wait \
  --for=jsonpath='{.status.phase}'=Running mongodb/${MDB_RESOURCE_NAME} --timeout=400s
echo; echo "MongoDB Enterprise resource"
kubectl --context "${K8S_CTX}" -n "${MDB_NS}" get mongodb/${MDB_RESOURCE_NAME}
echo; echo "Pods running in cluster ${K8S_CTX}"
kubectl --context "${K8S_CTX}" -n "${MDB_NS}" get pods
