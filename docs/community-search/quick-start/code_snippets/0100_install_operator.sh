helm upgrade --install --debug --kube-context "${K8S_CLUSTER_0_CONTEXT_NAME}" \
  --create-namespace \
  --namespace="${MDB_NAMESPACE}" \
  mongodb-kubernetes \
  --set "${OPERATOR_ADDITIONAL_HELM_VALUES:-"dummy=value"}" \
  "${OPERATOR_HELM_CHART}"
