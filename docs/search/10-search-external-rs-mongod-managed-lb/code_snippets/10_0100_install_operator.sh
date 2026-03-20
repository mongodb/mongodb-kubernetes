echo "Installing MongoDB Kubernetes Operator..."

# shellcheck disable=SC2086
helm upgrade --install mongodb-kubernetes-operator "${OPERATOR_HELM_CHART}" \
  --namespace "${MDB_NS}" \
  --kube-context "${K8S_CTX}" \
  --wait \
  --timeout 5m \
  ${OPERATOR_ADDITIONAL_HELM_VALUES:+--set ${OPERATOR_ADDITIONAL_HELM_VALUES}}

kubectl rollout status deployment/mongodb-kubernetes-operator \
  --namespace "${MDB_NS}" \
  --context "${K8S_CTX}" \
  --timeout=120s

echo "✓ MongoDB Kubernetes Operator installed and ready"
