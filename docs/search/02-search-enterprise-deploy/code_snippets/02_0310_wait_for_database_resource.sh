echo "Waiting for MongoDB resource to reach Running phase..."
kubectl --context "${K8S_CTX}" -n "${MDB_NS}" wait --for=jsonpath='{.status.phase}'=Running mdb/mdb-rs --timeout=400s
echo; echo "MongoDB resource"
kubectl --context "${K8S_CTX}" -n "${MDB_NS}" get mdb/mdb-rs
echo; echo "Pods running in cluster ${K8S_CTX}"
kubectl --context "${K8S_CTX}" -n "${MDB_NS}" get pods
