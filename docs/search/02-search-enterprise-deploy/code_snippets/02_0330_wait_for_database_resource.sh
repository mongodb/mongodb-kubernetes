echo "Waiting for MongoDB resource to reach Running phase..."
kubectl --context "${K8S_CTX}" -n "${MDB_NS}" wait --for=jsonpath='{.status.phase}'=Running mdb/mdb-rs --timeout=400s
