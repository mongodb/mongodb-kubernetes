kubectl --context "${K8S_CTX}" -n "${MDB_NS}" rollout status --timeout=2m deployment/mongodb-kubernetes-operator
echo "Operator deployment in ${OPERATOR_NAMESPACE} namespace"
kubectl --context "${K8S_CTX}" -n "${MDB_NS}" get deployments
echo; echo "Operator pod in ${OPERATOR_NAMESPACE} namespace"
kubectl --context "${K8S_CTX}" -n "${MDB_NS}" get pods
