echo "Waiting for MongoDBCommunity resource to reach Running phase..."
kubectl --context "${K8S_CTX}" -n "${MDB_NS}" wait \
  --for=jsonpath='{.status.phase}'=Running mdbc/mdbc-rs --timeout=400s
echo "Waiting for MongoDBCommunity resource (auto embedding) to reach Running phase..."
kubectl --context "${K8S_CTX}" -n "${MDB_NS}" wait \
  --for=jsonpath='{.status.phase}'=Running mdbc/"${MDB_RESOURCE_NAME_AUTO_EMBEDDING}" --timeout=400s
echo; echo "MongoDBCommunity resources"
kubectl --context "${K8S_CTX}" -n "${MDB_NS}" get mdbc
echo; echo "Pods running in cluster ${K8S_CTX}"
kubectl --context "${K8S_CTX}" -n "${MDB_NS}" get pods
