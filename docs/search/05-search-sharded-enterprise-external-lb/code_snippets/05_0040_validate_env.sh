# Validate required environment variables
required_vars=(
  "K8S_CTX"
  "MDB_NS"
  "MDB_RESOURCE_NAME"
  "MDB_SHARD_COUNT"
  "MDB_MONGODS_PER_SHARD"
  "MDB_MONGOS_COUNT"
  "MDB_CONFIG_SERVER_COUNT"
  "MDB_VERSION"
  "OPS_MANAGER_API_URL"
  "OPS_MANAGER_API_USER"
  "OPS_MANAGER_API_KEY"
  "OPS_MANAGER_ORG_ID"
  "OPS_MANAGER_PROJECT_NAME"
)

for var in "${required_vars[@]}"; do
  if [[ -z "${!var:-}" ]]; then
    echo "Error: Required environment variable ${var} is not set"
    exit 1
  fi
done

# Default MDB_MONGOT_REPLICAS to 1 if not set
export MDB_MONGOT_REPLICAS="${MDB_MONGOT_REPLICAS:-1}"

echo "All required environment variables are set"
echo "Sharded cluster configuration:"
echo "  Shards: ${MDB_SHARD_COUNT}"
echo "  Mongods per shard: ${MDB_MONGODS_PER_SHARD}"
echo "  Mongot replicas per shard: ${MDB_MONGOT_REPLICAS}"

if [[ "${MDB_MONGOT_REPLICAS}" -gt 1 ]]; then
  echo "  Note: Multiple mongot replicas configured - external LB endpoints will be used"
fi
