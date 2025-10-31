helm repo add jetstack https://charts.jetstack.io --force-update

helm upgrade --install \
  cert-manager jetstack/cert-manager \
  --kube-context "${K8S_CTX}" \
  --namespace "${CERT_MANAGER_NAMESPACE}" \
  --create-namespace \
  --set crds.enabled=true
