echo "Installing cert-manager (cluster 0 only -- it never runs on cluster 1 in this scenario)..."

if kubectl get namespace "${CERT_MANAGER_NAMESPACE}" --context "${K8S_CLUSTER_0_CONTEXT_NAME}" &>/dev/null \
  && kubectl get deployment cert-manager -n "${CERT_MANAGER_NAMESPACE}" --context "${K8S_CLUSTER_0_CONTEXT_NAME}" &>/dev/null; then
  echo "cert-manager is already installed, skipping installation"
  kubectl rollout status deployment/cert-manager -n "${CERT_MANAGER_NAMESPACE}" --context "${K8S_CLUSTER_0_CONTEXT_NAME}" --timeout=60s
else
  kubectl apply -f https://github.com/cert-manager/cert-manager/releases/latest/download/cert-manager.yaml \
    --context "${K8S_CLUSTER_0_CONTEXT_NAME}"

  echo "Waiting for cert-manager to be ready..."
  kubectl rollout status deployment/cert-manager -n "${CERT_MANAGER_NAMESPACE}" --context "${K8S_CLUSTER_0_CONTEXT_NAME}" --timeout=120s
  kubectl rollout status deployment/cert-manager-webhook -n "${CERT_MANAGER_NAMESPACE}" --context "${K8S_CLUSTER_0_CONTEXT_NAME}" --timeout=120s
  kubectl rollout status deployment/cert-manager-cainjector -n "${CERT_MANAGER_NAMESPACE}" --context "${K8S_CLUSTER_0_CONTEXT_NAME}" --timeout=120s
fi

echo "[ok] cert-manager ready in cluster ${K8S_CLUSTER_0_CONTEXT_NAME}"
