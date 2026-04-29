echo "Installing MongoDB Kubernetes Operator (multi-cluster mode) on the central cluster..."

helm upgrade --install mongodb-kubernetes-operator-multi-cluster \
  "${OPERATOR_HELM_CHART}" \
  --kube-context "${K8S_CENTRAL_CTX}" \
  --namespace "${OPERATOR_NAMESPACE}" \
  --set namespace="${OPERATOR_NAMESPACE}" \
  --set operator.namespace="${OPERATOR_NAMESPACE}" \
  --set operator.watchNamespace="${MDB_NS}" \
  --set operator.name=mongodb-kubernetes-operator-multi-cluster \
  --set operator.createOperatorServiceAccount=false \
  --set operator.createResourcesServiceAccountsAndRoles=false \
  --set "multiCluster.clusters={${K8S_CLUSTER_0_CTX},${K8S_CLUSTER_1_CTX}}" \
  --set "${OPERATOR_ADDITIONAL_HELM_VALUES:-dummy=value}" \
  --wait \
  --timeout 5m

kubectl rollout status deployment/mongodb-kubernetes-operator-multi-cluster \
  --namespace "${OPERATOR_NAMESPACE}" \
  --context "${K8S_CENTRAL_CTX}" \
  --timeout=120s

echo "[ok] Operator installed and ready"
