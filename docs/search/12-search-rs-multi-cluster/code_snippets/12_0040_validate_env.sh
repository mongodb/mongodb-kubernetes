echo "Validating environment variables..."

required_vars=(
  "K8S_CTX_0"
  "K8S_CTX_1"
  "MDB_NS"
  "MDB_RESOURCE_NAME"
  "MDB_SEARCH_RESOURCE_NAME"
  "MDB_VERSION"
  "MDB_ADMIN_USER_PASSWORD"
  "MDB_USER_PASSWORD"
  "MDB_SEARCH_SYNC_USER_PASSWORD"
  "MDB_MEMBERS_PER_CLUSTER"
  "MDB_MONGOT_REPLICAS_PER_CLUSTER"
  "MDB_PROXY_HOST_0"
  "MDB_RS_HOST_0_0"
  "MDB_RS_HOST_0_1"
  "MDB_RS_HOST_1_0"
  "MDB_RS_HOST_1_1"
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
for ctx in "${K8S_CTX_0:-}" "${K8S_CTX_1:-}"; do
  if [[ -n "${ctx}" ]] && ! kubectl config get-contexts "${ctx}" &>/dev/null; then
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
  echo "  Kubernetes contexts: ${K8S_CTX_0}, ${K8S_CTX_1}"
  echo "  Namespace: ${MDB_NS}"
  echo "  Resource name: ${MDB_RESOURCE_NAME}"
  echo "  Search resource name: ${MDB_SEARCH_RESOURCE_NAME}"
  echo "  RS members per cluster: ${MDB_MEMBERS_PER_CLUSTER}"
  echo "  Mongot replicas per cluster: ${MDB_MONGOT_REPLICAS_PER_CLUSTER}"
  echo "  Cluster 0 proxy host: ${MDB_PROXY_HOST_0}"
fi
