helm upgrade --install mongodb-kubernetes-operator "${OPERATOR_HELM_CHART}" \
  --kube-context "${K8S_CTX}" \
  --namespace "${MDB_NS}" \
  --set operator.watchNamespace="${MDB_NS}" \
  ${OPERATOR_ADDITIONAL_HELM_VALUES:+--set ${OPERATOR_ADDITIONAL_HELM_VALUES}}

