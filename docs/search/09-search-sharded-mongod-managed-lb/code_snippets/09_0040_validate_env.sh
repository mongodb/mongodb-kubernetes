set -eou pipefail

echo "Validating environment variables..."

required_vars=(
  "K8S_CTX"
  "MDB_NS"
  "MDB_RESOURCE_NAME"
  "MDB_VERSION"
  "MDB_ADMIN_USER_PASSWORD"
  "MDB_USER_PASSWORD"
  "MDB_SEARCH_SYNC_USER_PASSWORD"
  "MDB_SHARD_COUNT"
  "MDB_SHARD_0_NAME"
  "MDB_SHARD_1_NAME"
  "MDB_MONGOS_HOST"
  "MDB_MONGOT_REPLICAS"
  "MDB_PROXY_SVC_SHARD_0"
  "MDB_PROXY_SVC_SHARD_1"
  "MDB_ADMIN_CONNECTION_STRING"
  "MDB_USER_CONNECTION_STRING"
)

missing_vars=()

for var in "${required_vars[@]}"; do
  if [[ -z "${!var:-}" ]] || [[ "${!var}" == "<"* ]]; then
    missing_vars+=("${var}")
  fi
done

if [[ ${#missing_vars[@]} -gt 0 ]]; then
  echo "ERROR: The following required variables are not set or have placeholder values:"
  for var in "${missing_vars[@]}"; do
    echo "  - ${var}"
  done
  echo ""
  echo "Please edit env_variables.sh and set these values before proceeding."
  exit 1
fi

if ! kubectl config get-contexts "${K8S_CTX}" &>/dev/null; then
  echo "ERROR: Kubernetes context '${K8S_CTX}' does not exist."
  echo "Available contexts:"
  kubectl config get-contexts -o name
  exit 1
fi

echo "[ok] All required environment variables are set"
echo "  Kubernetes context: ${K8S_CTX}"
echo "  Namespace: ${MDB_NS}"
echo "  Resource name: ${MDB_RESOURCE_NAME}"
echo "  Shard count: ${MDB_SHARD_COUNT}"
echo "  Shard 0: ${MDB_SHARD_0_NAME}"
echo "  Shard 1: ${MDB_SHARD_1_NAME}"
echo "  Mongos: ${MDB_MONGOS_HOST}"
echo "  Mongot replicas: ${MDB_MONGOT_REPLICAS}"
