echo "Installing cert-manager..."

# cert-manager is only needed on the central cluster: all certificates are
# issued there and the resulting Secrets are replicated to the member clusters
# (by the operator for the source cert, by 12_0317 for the Search certs).
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
