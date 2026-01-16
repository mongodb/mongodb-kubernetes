#!/usr/bin/env bash
set -Eeou pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="${SCRIPT_DIR}/../.."
CONTEXT_ENV="${PROJECT_DIR}/.generated/context.env"

# Patterns for operator-relevant vars
OPERATOR_VAR_PATTERNS='(NAMESPACE|WATCH_NAMESPACE|OPERATOR_|MDB_|INIT_|KUBECONFIG|KUBE_CONFIG|DATABASE_|MONGODB_|OPS_MANAGER_|READINESS_|VERSION_UPGRADE_|PERFORM_|AGENT_)'

# Helm-only defaults (not in context files, have sensible defaults)
print_helm_defaults() {
  echo "IMAGE_PULL_POLICY=\"Always\""
  echo "IMAGE_PULL_SECRETS=\"image-registries-secret\""
  echo "OPS_MANAGER_IMAGE_PULL_POLICY=\"Always\""
  echo "MDB_DEFAULT_ARCHITECTURE=\"non-static\""
  echo "MDB_OPERATOR_TELEMETRY_SEND_ENABLED=\"false\""
  echo "MONGODB_IMAGE=\"mongodb-enterprise-server\""
}

# Filter context.env for operator-relevant vars
print_context_vars() {
  if [[ -f "${CONTEXT_ENV}" ]]; then
    grep -E "^${OPERATOR_VAR_PATTERNS}" "${CONTEXT_ENV}" | grep -v "^#" || true
  fi
}

print_helm_defaults
print_context_vars
