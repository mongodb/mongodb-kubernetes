helm upgrade --install --debug --kube-context "${K8S_CTX}" \
  --create-namespace \
  --namespace="${MDB_NS}" \
  mongodb-kubernetes \
  ${OPERATOR_ADDITIONAL_HELM_VALUES:+--set ${OPERATOR_ADDITIONAL_HELM_VALUES}} \
  "${OPERATOR_HELM_CHART}"

echo "Waiting for the operator deployment to become available..."
kubectl rollout status deployment/mongodb-kubernetes-operator \
  --context "${K8S_CTX}" -n "${MDB_NS}" --timeout=300s

echo "[ok] Operator installed in namespace '${MDB_NS}'"
