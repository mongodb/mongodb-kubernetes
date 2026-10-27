echo "Validating environment variables..."

required_vars=(
  "K8S_CTX_0"
  "K8S_CTX_1"
  "K8S_CTX_2"
  "MDB_NS"
  "OPERATOR_HELM_CHART"
  "OM_VERSION"
  "APPDB_VERSION"
  "MANAGEMENT_OM_NAME"
  "PRIMARY_OM_NAME"
  "APPDB_NAME"
  "MANAGEMENT_OM_URL"
  "MANAGEMENT_OM_ADMIN_KEY_SECRET"
  "APPDB_PROJECT_NAME"
  "APPDB_PROJECT_CONFIGMAP"
  "APPDB_CA_CONFIGMAP"
  "APPDB_CERT_PREFIX"
  "OM_ADMIN_PASSWORD"
)

missing_vars=()
for var in "${required_vars[@]}"; do
  [[ -n "${!var:-}" ]] && [[ "${!var}" != "<"* ]] || missing_vars+=("${var}")
done

has_error=false
if (( ${#missing_vars[@]} )); then
  echo "ERROR: Missing required environment variables:" >&2
  for m in "${missing_vars[@]}"; do echo "  - ${m}" >&2; done
  echo "Please edit env_variables.sh and set these values before proceeding." >&2
  has_error=true
fi

missing_contexts=()
for ctx in "${K8S_CTX_0:-}" "${K8S_CTX_1:-}" "${K8S_CTX_2:-}"; do
  if [[ -n "${ctx}" ]] && [[ "${ctx}" != "<"* ]] && ! kubectl config get-contexts "${ctx}" &>/dev/null; then
    missing_contexts+=("${ctx}")
  fi
done

if (( ${#missing_contexts[@]} )); then
  echo "ERROR: Kubernetes context(s) not found:" >&2
  for ctx in "${missing_contexts[@]}"; do echo "  - ${ctx}" >&2; done
  echo "Available contexts:" >&2
  kubectl config get-contexts -o name >&2
  has_error=true
fi

if [[ "${has_error}" == "true" ]]; then
  false
else
  echo "[ok] All required environment variables are set"
  echo "  Central cluster: ${K8S_CTX_0}"
  echo "  AppDB member clusters: ${K8S_CTX_1}, ${K8S_CTX_2}"
  echo "  Namespace: ${MDB_NS}"
fi
