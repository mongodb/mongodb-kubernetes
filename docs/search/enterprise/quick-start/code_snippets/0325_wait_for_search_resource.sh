echo "Waiting for MongoDBSearch resource to reach Running phase..."
kubectl --context "${K8S_CLUSTER_0_CONTEXT_NAME}" -n "${MDB_NAMESPACE}" wait --for=jsonpath='{.status.phase}'=Running mdbs/mdb-rs --timeout=300s
