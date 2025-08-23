echo; echo "MongoDB resource"
kubectl --context "${K8S_CTX}" -n "${MDB_NS}" get mdb/mdb-rs
echo; echo "MongoDBSearch resource"
kubectl --context "${K8S_CTX}" -n "${MDB_NS}" get mdbs/mdb-rs
echo; echo "Pods running in cluster ${K8S_CTX}"
kubectl --context "${K8S_CTX}" -n "${MDB_NS}" get pods
