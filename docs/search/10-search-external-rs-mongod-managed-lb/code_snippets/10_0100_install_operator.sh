#!/usr/bin/env bash
# Install the MongoDB Kubernetes Operator

helm_values=""
if [[ -n "${OPERATOR_ADDITIONAL_HELM_VALUES:-}" ]]; then
  IFS=',' read -ra VALUES <<< "${OPERATOR_ADDITIONAL_HELM_VALUES}"
  for val in "${VALUES[@]}"; do
    helm_values="${helm_values} --set ${val}"
  done
fi

echo "Installing MongoDB Kubernetes Operator..."

# shellcheck disable=SC2086
helm upgrade --install mongodb-kubernetes-operator "${OPERATOR_HELM_CHART}" \
  --namespace "${MDB_NS}" \
  --kube-context "${K8S_CTX}" \
  --wait \
  --timeout 5m \
  ${helm_values}

kubectl rollout status deployment/mongodb-kubernetes-operator \
  --namespace "${MDB_NS}" \
  --context "${K8S_CTX}" \
  --timeout=120s

echo "✓ MongoDB Kubernetes Operator installed and ready"
