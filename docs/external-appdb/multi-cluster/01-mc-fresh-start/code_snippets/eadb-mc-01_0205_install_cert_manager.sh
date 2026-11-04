# The multi-cluster AppDB members are TLS-enabled, so we need cert-manager to issue their server
# certificate. Install it on the central cluster (where the AppDB Certificate resources are created;
# the operator distributes the resulting cert material to the member clusters).
echo "Installing cert-manager..."

if kubectl get namespace "${CERT_MANAGER_NAMESPACE}" --context "${K8S_CTX_0}" &>/dev/null \
  && kubectl get deployment cert-manager -n "${CERT_MANAGER_NAMESPACE}" --context "${K8S_CTX_0}" &>/dev/null; then
  echo "cert-manager is already installed, skipping installation"
  kubectl rollout status deployment/cert-manager -n "${CERT_MANAGER_NAMESPACE}" --context "${K8S_CTX_0}" --timeout=60s
  echo "[ok] cert-manager is ready"
else
  kubectl apply -f https://github.com/cert-manager/cert-manager/releases/latest/download/cert-manager.yaml \
    --context "${K8S_CTX_0}"

  echo "Waiting for cert-manager to be ready..."
  kubectl rollout status deployment/cert-manager -n "${CERT_MANAGER_NAMESPACE}" --context "${K8S_CTX_0}" --timeout=300s
  kubectl rollout status deployment/cert-manager-webhook -n "${CERT_MANAGER_NAMESPACE}" --context "${K8S_CTX_0}" --timeout=300s
  kubectl rollout status deployment/cert-manager-cainjector -n "${CERT_MANAGER_NAMESPACE}" --context "${K8S_CTX_0}" --timeout=300s

  echo "[ok] cert-manager installed and ready"
fi
