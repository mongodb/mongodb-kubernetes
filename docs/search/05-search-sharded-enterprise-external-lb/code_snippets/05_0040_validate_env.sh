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

echo "All required environment variables are set"

