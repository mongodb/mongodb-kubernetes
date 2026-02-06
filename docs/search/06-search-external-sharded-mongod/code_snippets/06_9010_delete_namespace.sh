# Cleanup - delete the namespace and all resources
kubectl delete namespace "${MDB_NS}" --context "${K8S_CTX}" --ignore-not-found

