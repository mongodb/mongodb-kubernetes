# Validate required environment variables
required_vars=(
  "K8S_CTX"
  "MDB_NS"
  "MDB_VERSION"
  "MDB_EXTERNAL_CLUSTER_NAME"
  "MDB_SHARD_COUNT"
  "MDB_MONGODS_PER_SHARD"
  "MDB_MONGOS_COUNT"
  "MDB_CONFIG_SERVER_COUNT"
  "MDB_SEARCH_RESOURCE_NAME"
  "MDB_ADMIN_USER_PASSWORD"
  "MDB_USER_PASSWORD"
  "MDB_SEARCH_SYNC_USER_PASSWORD"
)

for var in "${required_vars[@]}"; do
  if [[ -z "${!var:-}" ]]; then
    echo "Error: Required environment variable ${var} is not set"
    exit 1
  fi
done

echo "All required environment variables are set"
echo "External sharded cluster configuration:"
echo "  Cluster name: ${MDB_EXTERNAL_CLUSTER_NAME}"
echo "  Shards: ${MDB_SHARD_COUNT}"
echo "  Mongods per shard: ${MDB_MONGODS_PER_SHARD}"
echo "  Mongos count: ${MDB_MONGOS_COUNT}"
echo "  Config server count: ${MDB_CONFIG_SERVER_COUNT}"
echo "  Search resource name: ${MDB_SEARCH_RESOURCE_NAME}"

