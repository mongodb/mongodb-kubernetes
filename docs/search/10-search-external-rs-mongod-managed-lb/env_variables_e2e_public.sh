# E2E Test Environment - Public Configuration
#
# Sources the default environment and overrides
# CI-specific values for public testing.

source "$(dirname "${BASH_SOURCE[0]}")/env_variables.sh"

export K8S_CTX="kind-kind"

export OPS_MANAGER_PROJECT_NAME="${NAMESPACE}-${MDB_EXTERNAL_CLUSTER_NAME}"
export OPS_MANAGER_API_URL="${OM_BASE_URL}"
export OPS_MANAGER_API_USER="${OM_USER}"
export OPS_MANAGER_API_KEY="${OM_API_KEY}"
export OPS_MANAGER_ORG_ID="${OM_ORGID}"
