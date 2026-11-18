# E2E Test Environment - Common Configuration
#
# Shared variables for both public and private E2E testing.
# Sourced by env_variables_e2e_public.sh and env_variables_e2e_private.sh.

# The external AppDB is an Ops-Manager-managed MongoDB, so its version (APPDB_VERSION) must be a
# MongoDB version the management OM actually offers in its version manifest. The shared private/dev
# e2e context (variables/omXX) sets CUSTOM_OM_VERSION and CUSTOM_APPDB_VERSION together from the same
# OM line, so they are already consistent -> use CUSTOM_OM_VERSION as-is. The public flavor leaves
# CUSTOM_OM_VERSION unset and keeps the customer-facing OM_VERSION from env_variables.sh.
if [[ -n "${CUSTOM_OM_VERSION:-}" ]]; then
  OM_VERSION="${CUSTOM_OM_VERSION}"
fi
export OM_VERSION
export APPDB_VERSION="${CUSTOM_APPDB_VERSION:-${APPDB_VERSION}}"
