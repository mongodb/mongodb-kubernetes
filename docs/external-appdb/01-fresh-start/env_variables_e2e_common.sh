# E2E Test Environment - Common Configuration
#
# Shared variables for both public and private E2E testing.
# Sourced by env_variables_e2e_public.sh and env_variables_e2e_private.sh.

# The external AppDB is an Ops-Manager-managed MongoDB, so its version (APPDB_VERSION) must be a
# MongoDB version the management OM actually offers in its version manifest. The shared private/dev
# e2e context (scripts/dev/contexts/e2e_mdb_kind_ubi_cloudqa) pins CUSTOM_OM_VERSION to the 7.0 line,
# which does NOT list 8.0.x MongoDB versions -> the external AppDB fails to reconcile with
# "Invalid config: MongoDB version 8.0.5-ent is not available". The private_kind_code_snippets
# variant already builds OM 8.0 images (base_om8_dependency), so when running under a dev context
# pin the management OM to the 8.0 line (read from the same anchor the contexts use) to keep it
# consistent with APPDB_VERSION. The public flavor leaves CUSTOM_OM_VERSION unset and keeps the
# customer-facing OM_VERSION from env_variables.sh.
if [[ -n "${CUSTOM_OM_VERSION:-}" && -n "${PROJECT_DIR:-}" ]]; then
  OM_VERSION=$(grep -E "^\s*-\s*&ops_manager_80_latest\s+(\S+)\s+#" "${PROJECT_DIR}/.evergreen.yml" | awk '{print $3}')
fi
export OM_VERSION
export APPDB_VERSION="${CUSTOM_APPDB_VERSION:-${APPDB_VERSION}}"
