echo; echo "MongoDBCommunity resources"
kubectl --context "${K8S_CTX}" -n "${MDB_NS}" get mdbc
echo; echo "MongoDBSearch resources"
kubectl --context "${K8S_CTX}" -n "${MDB_NS}" get mdbs
echo; echo "Pods running in cluster ${K8S_CTX}"
kubectl --context "${K8S_CTX}" -n "${MDB_NS}" get pods
