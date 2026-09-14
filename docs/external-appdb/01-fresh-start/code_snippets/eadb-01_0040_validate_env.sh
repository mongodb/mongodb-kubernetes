echo "Validating environment variables..."

required_vars=(
  "K8S_CTX"
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
  "OM_ADMIN_PASSWORD"
)

missing_vars=()
for var in "${required_vars[@]}"; do
  [[ -n "${!var:-}" ]] && [[ "${!var}" != "<"* ]] || missing_vars+=("${var}")
done

if (( ${#missing_vars[@]} )); then
  echo "ERROR: Missing required environment variables:" >&2
  for m in "${missing_vars[@]}"; do echo "  - ${m}" >&2; done
  echo "Please edit env_variables.sh and set these values before proceeding." >&2
  return 1 2>/dev/null || exit 1
elif ! kubectl config get-contexts "${K8S_CTX}" &>/dev/null; then
  echo "ERROR: Kubernetes context '${K8S_CTX}' does not exist." >&2
  kubectl config get-contexts -o name
  return 1 2>/dev/null || exit 1
else
  echo "[ok] All required environment variables are set"
fi
