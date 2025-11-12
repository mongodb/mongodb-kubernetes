#!/usr/bin/env bash

set -euo pipefail

required_env=(
  "K8S_CTX"
  "CERT_MANAGER_NAMESPACE"
)

for var in "${required_env[@]}"; do
  if [[ -z "${!var:-}" ]]; then
    echo "Environment variable ${var} must be set" >&2
    exit 1
  fi
done

helm upgrade --install \
  cert-manager \
  oci://quay.io/jetstack/charts/cert-manager \
  --kube-context "${K8S_CTX}" \
  --namespace "${CERT_MANAGER_NAMESPACE}" \
  --create-namespace \
  --set crds.enabled=true

for deployment in cert-manager cert-manager-cainjector cert-manager-webhook; do
  kubectl --context "${K8S_CTX}" \
    -n "${CERT_MANAGER_NAMESPACE}" \
    wait --for=condition=Available "deployment/${deployment}" --timeout=300s
done

echo "cert-manager is ready in namespace ${CERT_MANAGER_NAMESPACE}."
