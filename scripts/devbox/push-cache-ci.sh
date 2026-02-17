#!/usr/bin/env bash
# push-cache-ci.sh
#
# CI-specific wrapper for pushing devbox dependencies to the S3 cache.
# Intended to run as an Evergreen task on master merges (not on every patch).
#
# This task:
#   1. Expects devbox to be installed (via setup_building_host_with_devbox)
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

# --- Ensure Nix is on PATH ---
# When running in a CI subprocess.exec, Nix may not be on PATH yet.
if ! command -v nix &>/dev/null; then
    if [[ -f "/nix/var/nix/profiles/default/etc/profile.d/nix-daemon.sh" ]]; then
        # shellcheck disable=SC1091
        . "/nix/var/nix/profiles/default/etc/profile.d/nix-daemon.sh"
    fi
fi

echo "============================================"
echo "  MCK Nix Cache Push (CI)"
echo "============================================"
echo ""

# Navigate to project root
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
cd "${PROJECT_ROOT}"

# --- Ensure devbox is installed and packages are present ---

echo "Ensuring devbox packages are installed..."
devbox install 2>&1 | tail -5
echo ""

# --- Push to S3 ---

"${SCRIPT_DIR}/push-cache.sh"

echo ""
echo "Cache push complete. All Evergreen workers will use the updated cache."
