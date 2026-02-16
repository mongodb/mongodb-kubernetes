#!/usr/bin/env bash
# configure-nix-cache.sh
#
# Configure Nix to use the MCK S3 binary cache as a substituter.
# Works on both macOS (local dev) and Linux/Ubuntu (Evergreen CI).
#
# This script writes /etc/nix/nix.custom.conf (NOT nix.conf) so that we never
# conflict with the base Nix configuration managed by the Nix installer.
# The base nix.conf must include:
#   !include nix.custom.conf
# to pick up our settings. The Determinate Systems installer already has this.
#
# The S3 cache is listed first with priority=10 so it is resolved before
# cache.nixos.org (priority=40). Lower number = higher priority.
#
# AWS Authentication:
#   Nix uses the standard AWS credential chain for S3 access:
#     1. Environment variables (AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY / AWS_SESSION_TOKEN)
#     2. AWS config/credentials file (~/.aws/credentials, profile support)
#     3. EC2 instance profile / IAM role (automatic on Evergreen hosts)
#     4. AWS SSO (via `aws sso login`)
#
#   IMPORTANT — macOS multi-user Nix:
#     The Nix daemon runs as root. It reads /var/root/.aws/credentials, NOT the
#     current user's ~/.aws/credentials. To make S3 access work locally:
#       sudo mkdir -p /var/root/.aws
#       sudo cp ~/.aws/credentials /var/root/.aws/credentials
#       sudo cp ~/.aws/config      /var/root/.aws/config
#     Repeat after credential rotation (e.g., `aws sso login`).
#
#   For Evergreen CI (Linux): IAM instance profile attached to EC2 workers
#
# Usage:
#   ./scripts/devbox/configure-nix-cache.sh
#
# Options (via environment):
#   MCK_NIX_CACHE_BUCKET     - S3 bucket name (default: mck-nix-cache)
#   MCK_NIX_CACHE_REGION     - AWS region (default: us-east-1)
#   MCK_NIX_CACHE_PUBLIC_KEY - Public verification key
#
set -Eeou pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/nix-cache.env"

# --- Always write to /etc/nix (system-level) ---
# On macOS with multi-user Nix the daemon reads /etc/nix/nix.conf.
# On Linux (Evergreen) the same path is used.
# Writing to both system and user level causes duplication, so we only
# use the system level.

NIX_CONF_DIR="/etc/nix"
NIX_CUSTOM_CONF_PATH="${NIX_CONF_DIR}/nix.custom.conf"
NIX_CONF_PATH="${NIX_CONF_DIR}/nix.conf"

# --- Validate prerequisites ---

if ! command -v nix &>/dev/null; then
    echo "ERROR: Nix is not installed."
    echo "  Install Nix first:  ./scripts/devbox/install-devbox.sh"
    echo "  Or manually:        curl --proto '=https' --tlsv1.2 -sSf -L https://install.determinate.systems/nix | sh -s -- install"
    exit 1
fi

if [[ "${MCK_NIX_CACHE_PUBLIC_KEY}" == *"REPLACE_WITH_REAL"* ]]; then
    echo "WARNING: MCK_NIX_CACHE_PUBLIC_KEY is still set to the placeholder value."
    echo "         Generate keys first: ./scripts/devbox/generate-cache-keys.sh"
    echo "         Then update scripts/devbox/nix-cache.env with the real public key."
    echo ""
    echo "Continuing without S3 cache configuration (will use cache.nixos.org only)."
    exit 0
fi

# --- Configure nix.custom.conf ---

echo "Configuring Nix binary cache..."
echo "  Custom config: ${NIX_CUSTOM_CONF_PATH}"
echo "  S3 bucket:     s3://${MCK_NIX_CACHE_BUCKET} (${MCK_NIX_CACHE_REGION})"

sudo mkdir -p "${NIX_CONF_DIR}"

# Build the S3 substituter URL
# scheme=https ensures TLS. priority=10 so S3 is tried before cache.nixos.org (40).
S3_SUBSTITUTER="s3://${MCK_NIX_CACHE_BUCKET}?region=${MCK_NIX_CACHE_REGION}&scheme=https&priority=10"
NIXOS_SUBSTITUTER="https://cache.nixos.org?priority=40"

# Use `substituters` (not `extra-substituters`) to control the full ordered list.
# S3 is first so it appears before cache.nixos.org in both listing and priority.
NIX_CUSTOM_CONF="# nix.custom.conf - MCK S3 Nix Binary Cache
# Managed by scripts/devbox/configure-nix-cache.sh — do not edit manually.
# This file is included from nix.conf via: !include nix.custom.conf

# S3 cache listed first with priority=10 (< nixos 40) — checked before cache.nixos.org.
substituters = ${S3_SUBSTITUTER} ${NIXOS_SUBSTITUTER}
trusted-public-keys = ${MCK_NIX_CACHE_PUBLIC_KEY} cache.nixos.org-1:6NCHdD59X431o0gWypbMrAURkbJ16ZPMQFGspcDShjY=

# Allow the current user to add substituters without being in trusted-users.
# This is needed on multi-user Nix installations (default on macOS).
trusted-substituters = ${S3_SUBSTITUTER}

# Increase download parallelism for faster cache fetches
max-substitution-jobs = 16

# Accept flake configuration from devbox
accept-flake-config = true
"

# --- Write nix.custom.conf ---

write_file() {
    local dest="$1"
    local content="$2"

    local tmp_file
    tmp_file="$(mktemp)"
    echo "${content}" > "${tmp_file}"
    sudo cp "${tmp_file}" "${dest}"
    sudo chmod 644 "${dest}"
    rm -f "${tmp_file}"
}

write_file "${NIX_CUSTOM_CONF_PATH}" "${NIX_CUSTOM_CONF}"
echo "  Wrote ${NIX_CUSTOM_CONF_PATH}"

# --- Ensure nix.conf includes nix.custom.conf ---

INCLUDE_DIRECTIVE="!include nix.custom.conf"

if [[ ! -f "${NIX_CONF_PATH}" ]]; then
    echo "  Creating ${NIX_CONF_PATH} with include directive."
    write_file "${NIX_CONF_PATH}" "${INCLUDE_DIRECTIVE}"
elif ! grep -qF "${INCLUDE_DIRECTIVE}" "${NIX_CONF_PATH}" 2>/dev/null; then
    echo "  Adding include directive to ${NIX_CONF_PATH}"
    _tmp_file="$(mktemp)"
    sudo cat "${NIX_CONF_PATH}" > "${_tmp_file}"
    echo "" >> "${_tmp_file}"
    echo "# Include MCK S3 cache configuration" >> "${_tmp_file}"
    echo "${INCLUDE_DIRECTIVE}" >> "${_tmp_file}"
    sudo cp "${_tmp_file}" "${NIX_CONF_PATH}"
    sudo chmod 644 "${NIX_CONF_PATH}"
    rm -f "${_tmp_file}"
else
    echo "  ${NIX_CONF_PATH} already includes nix.custom.conf"
fi

# --- Restart the Nix daemon to pick up the new config ---
#
# On macOS multi-user Nix, the daemon caches its config in memory.
# Without a restart it will keep using the old substituters.

echo ""
echo "Restarting Nix daemon to apply new configuration..."

OS_TYPE="$(uname -s)"
if [[ "${OS_TYPE}" == "Darwin" ]]; then
    if sudo launchctl kickstart -k system/systems.determinate.nix-daemon 2>/dev/null; then
        echo "  Restarted (Determinate Systems daemon)."
    elif sudo launchctl kickstart -k system/org.nixos.nix-daemon 2>/dev/null; then
        echo "  Restarted (NixOS daemon)."
    else
        echo "  WARNING: Could not restart daemon. You may need to restart manually:"
        echo "    sudo launchctl kickstart -k system/systems.determinate.nix-daemon"
    fi
elif [[ "${OS_TYPE}" == "Linux" ]]; then
    if sudo systemctl restart nix-daemon 2>/dev/null; then
        echo "  Restarted (systemd)."
    else
        echo "  WARNING: Could not restart nix-daemon via systemctl."
    fi
fi

echo ""
echo "=== Nix S3 cache configured ==="
echo ""
echo "Cache resolution order:"
echo "  1. /nix/store (local)"
echo "  2. s3://${MCK_NIX_CACHE_BUCKET} (team cache, priority=10)"
echo "  3. cache.nixos.org (public, priority=40)"
echo ""

# --- Verify AWS access ---

echo "Verifying AWS credentials for S3 access..."
if command -v aws &>/dev/null; then
    if aws sts get-caller-identity --region "${MCK_NIX_CACHE_REGION}" &>/dev/null; then
        echo "  AWS credentials: OK"
        IDENTITY=$(aws sts get-caller-identity --region "${MCK_NIX_CACHE_REGION}" --query 'Arn' --output text 2>/dev/null || echo "unknown")
        echo "  Identity: ${IDENTITY}"
    else
        echo "  WARNING: AWS credentials not configured or expired."
        echo "  S3 cache will be skipped until credentials are available."
        echo ""
        echo "  For local dev:    aws sso login"
        echo "  For Evergreen CI: ensure IAM instance profile is attached"
    fi
else
    echo "  WARNING: AWS CLI not found. Install it or enter devbox shell first."
    echo "  Nix will still attempt S3 access using env vars or instance profile."
fi

echo ""
echo "To verify the cache works (requires clean .devbox and nix store):"
echo "  rm -rf .devbox && sudo nix-collect-garbage -d"
echo "  devbox install 2>&1 | grep 'copying path' | grep -oE \"from '.*'\" | sort | uniq -c | sort -rn"
