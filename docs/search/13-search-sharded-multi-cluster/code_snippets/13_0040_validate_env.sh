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
  "MDB_SHARD_COUNT"
  "MDB_MONGODS_PER_SHARD_PER_CLUSTER"
  "MDB_MONGOS_PER_CLUSTER"
  "MDB_CONFIG_SERVERS_PER_CLUSTER"
  "MDB_MONGOT_REPLICAS_PER_CLUSTER"
  "MDB_PROXY_HOST_0"
  "MDB_PROXY_HOST_SHARD_0"
  "MDB_PROXY_HOST_SHARD_1"
  "MDB_PROXY_HOST_SHARD_2"
  "MDB_ADMIN_CONNECTION_STRING"
  "MDB_USER_CONNECTION_STRING"
)

missing_vars=()
for var in "${required_vars[@]}"; do
  [[ -n "${!var:-}" ]] && [[ "${!var}" != "<"* ]] || missing_vars+=("${var}")
done

if (( ${#missing_vars[@]} )); then
  echo "ERROR: Missing required environment variables:" >&2
  for m in "${missing_vars[@]}"; do echo "  - ${m}" >&2; done
  echo "Please edit env_variables.sh and set these values before proceeding." >&2
else
  for ctx in "${K8S_CTX_0}" "${K8S_CTX_1}"; do
    if ! kubectl config get-contexts "${ctx}" &>/dev/null; then
      echo "ERROR: Kubernetes context '${ctx}' does not exist." >&2
      kubectl config get-contexts -o name
      exit 1
    fi
  done
  echo "[ok] All required environment variables are set"
  echo "  Kubernetes contexts: ${K8S_CTX_0}, ${K8S_CTX_1}"
  echo "  Namespace: ${MDB_NS}"
  echo "  Resource name: ${MDB_RESOURCE_NAME}"
  echo "  Search resource name: ${MDB_SEARCH_RESOURCE_NAME}"
  echo "  Shards: ${MDB_SHARD_COUNT}"
  echo "  Mongot replicas per (cluster, shard): ${MDB_MONGOT_REPLICAS_PER_CLUSTER}"
  echo "  Cluster 0 mongos proxy host: ${MDB_PROXY_HOST_0}"
fi
