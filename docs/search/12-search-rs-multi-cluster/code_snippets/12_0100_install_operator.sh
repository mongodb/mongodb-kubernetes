echo "Installing the operator..."

helm upgrade --install --debug --kube-context "${K8S_CTX_0}" \
  --create-namespace --namespace="${MDB_NS}" \
  mongodb-kubernetes-operator-multi-cluster \
  --set namespace="${MDB_NS}" \
  --set operator.namespace="${MDB_NS}" \
  --set operator.watchNamespace="${MDB_NS}" \
  --set operator.name=mongodb-kubernetes-operator-multi-cluster \
  ${OPERATOR_ADDITIONAL_HELM_VALUES:+--set ${OPERATOR_ADDITIONAL_HELM_VALUES}} \
  "${OPERATOR_HELM_CHART}"

kubectl --context "${K8S_CTX_0}" -n "${MDB_NS}" rollout status \
  --timeout=2m deployment/mongodb-kubernetes-operator-multi-cluster

echo "[ok] Operator installed"
