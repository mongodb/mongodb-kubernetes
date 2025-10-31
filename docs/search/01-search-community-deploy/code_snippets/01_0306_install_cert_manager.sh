required=(
  K8S_CTX
  CERT_MANAGER_NAMESPACE
)
missing=()
for var in "${required[@]}"; do
  [[ -n "${!var:-}" ]] || missing+=("${var}")
done
if (( ${#missing[@]} )); then
  echo "Missing required environment variables: ${missing[*]}" >&2
  exit 1
fi

helm repo add jetstack https://charts.jetstack.io --force-update >/dev/null 2>&1 || true
helm upgrade --install \
  cert-manager jetstack/cert-manager \
  --kube-context "${K8S_CTX}" \
  --namespace "${CERT_MANAGER_NAMESPACE}" \
  --create-namespace \
  --set crds.enabled=true >/dev/null 2>&1

for deployment in cert-manager cert-manager-cainjector cert-manager-webhook; do
  kubectl --context "${K8S_CTX}" \
    -n "${CERT_MANAGER_NAMESPACE}" \
    wait --for=condition=Available "deployment/${deployment}" --timeout=300s
done

echo "cert-manager is ready in namespace ${CERT_MANAGER_NAMESPACE}."
