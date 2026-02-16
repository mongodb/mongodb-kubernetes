#!/usr/bin/env bash
# push-cache-ci.sh
#
# CI-specific wrapper for pushing devbox dependencies to the S3 cache.
# Intended to run as an Evergreen task on master merges (not on every patch).
#
# This task:
#   1. Sets up devbox (via setup-ci-cache.sh)
#   2. Signs and pushes all store paths to S3
#
# The private signing key must be passed via MCK_NIX_CACHE_PRIV_KEY env var.
# In Evergreen, set this as a project secret or expansion variable.
#
# Evergreen integration:
#   push_devbox_cache:
#     - *setup_devbox
#     - command: subprocess.exec
#       params:
#         working_dir: src/github.com/mongodb/mongodb-kubernetes
#         binary: scripts/devbox/push-cache-ci.sh
#         env:
#           MCK_NIX_CACHE_PRIV_KEY: ${nix_cache_signing_key}
#
# This should only run:
#   - On master merges (not on every patch build)
#   - When devbox.json or devbox.lock has changed
#
set -Eeou pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/nix-cache.env"

echo "============================================"
echo "  MCK Nix Cache Push (CI)"
echo "============================================"
echo ""

# Navigate to project root
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
cd "${PROJECT_ROOT}"

# --- Check if devbox.json changed (optional optimization) ---

# On Evergreen, we can check if devbox.json or devbox.lock changed in the
# current commit. If not, we can skip the push.
if command -v git &>/dev/null; then
    CHANGED_FILES=$(git diff --name-only HEAD~1 HEAD 2>/dev/null || echo "")
    if [[ -n "${CHANGED_FILES}" ]]; then
        if ! echo "${CHANGED_FILES}" | grep -qE 'devbox\.(json|lock)'; then
            echo "No changes to devbox.json or devbox.lock - skipping cache push."
            echo "(Force push by setting FORCE_CACHE_PUSH=true)"
            if [[ "${FORCE_CACHE_PUSH:-false}" != "true" ]]; then
                exit 0
            fi
        fi
    fi
fi

# --- Ensure devbox is installed and packages are present ---

echo "Ensuring devbox packages are installed..."
devbox install 2>&1 | tail -5
echo ""

# --- Push to S3 ---

"${SCRIPT_DIR}/push-cache.sh"

echo ""
echo "Cache push complete. All Evergreen workers will use the updated cache."
