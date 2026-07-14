# E2E Test Environment - Common Configuration
#
# Shared variables for both public and private E2E testing.
# Sourced by env_variables_e2e_public.sh and env_variables_e2e_private.sh.

# Ops Manager
export OPS_MANAGER_PROJECT_NAME="${NAMESPACE}-${MDB_EXTERNAL_CLUSTER_NAME}"
export OPS_MANAGER_API_URL="${OM_BASE_URL}"
export OPS_MANAGER_API_USER="${OM_USER}"
export OPS_MANAGER_API_KEY="${OM_API_KEY}"
export OPS_MANAGER_ORG_ID="${OM_ORGID}"
