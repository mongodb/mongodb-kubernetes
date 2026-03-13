#!/usr/bin/env bash
# Install cert-manager for TLS certificate management
#
# cert-manager automates the creation and renewal of TLS certificates.
# We use it to create certificates for:
# - MongoDB server TLS
# - mongot (MongoDB Search) server TLS
# - Envoy proxy TLS (automatically by the operator)

echo "Installing cert-manager..."

# Check if cert-manager is already installed
if kubectl get namespace "${CERT_MANAGER_NAMESPACE}" --context "${K8S_CTX}" &>/dev/null; then
  if kubectl get deployment cert-manager -n "${CERT_MANAGER_NAMESPACE}" --context "${K8S_CTX}" &>/dev/null; then
    echo "cert-manager is already installed, skipping installation"
    kubectl rollout status deployment/cert-manager -n "${CERT_MANAGER_NAMESPACE}" --context "${K8S_CTX}" --timeout=60s
    echo "✓ cert-manager is ready"
    exit 0
  fi
fi

# Install cert-manager using kubectl
kubectl apply -f https://github.com/cert-manager/cert-manager/releases/latest/download/cert-manager.yaml \
  --context "${K8S_CTX}"

# Wait for cert-manager deployments to be ready
echo "Waiting for cert-manager to be ready..."
kubectl rollout status deployment/cert-manager -n "${CERT_MANAGER_NAMESPACE}" --context "${K8S_CTX}" --timeout=120s
kubectl rollout status deployment/cert-manager-webhook -n "${CERT_MANAGER_NAMESPACE}" --context "${K8S_CTX}" --timeout=120s
kubectl rollout status deployment/cert-manager-cainjector -n "${CERT_MANAGER_NAMESPACE}" --context "${K8S_CTX}" --timeout=120s

echo "✓ cert-manager installed and ready"

