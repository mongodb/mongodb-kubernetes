#!/usr/bin/env bash
# configure-nix-cache.sh
#
# Configure Nix to use the MCK S3 binary cache as a substituter.
# Works on both macOS (local dev) and Linux/Ubuntu (Evergreen CI).
#
# What this script does:
#   1. (CI only) Writes ~/.aws/credentials from Evergreen expansion variables
#   2. Copies ~/.aws/credentials to root's home for the Nix daemon
#      - macOS: /var/root/.aws/   - Linux: /root/.aws/
#   3. Writes /etc/nix/nix.custom.conf with the S3 substituter
#   4. Restarts the Nix daemon to pick up the new config
#
# AWS Authentication:
#   Nix uses the standard AWS credential chain for S3 access:
#     - Environment variables (AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY)
#     - AWS config/credentials file (~/.aws/credentials)
#     - EC2 instance profile / IAM role
#     - AWS SSO (via `aws sso login`)
#
#   macOS (local dev):
#     The Nix daemon runs as root via launchd. It reads /var/root/.aws/, NOT
#     the current user's ~/.aws/. This script copies credentials automatically.
#     After credential rotation (e.g., `aws sso login`), re-run this script.
#
#   Evergreen CI (Linux):
#     Pass mms_eng_test_aws_access_key and mms_eng_test_aws_secret via
#     include_expansions_in_env. This script writes them to ~/.aws/credentials.
#
# Usage:
#   ./scripts/devbox/configure-nix-cache.sh
#
# Environment (optional):
#   MCK_NIX_CACHE_BUCKET              - S3 bucket name (default: mck-nix-cache)
#   MCK_NIX_CACHE_REGION              - AWS region (default: us-east-1)
#   MCK_NIX_CACHE_PUBLIC_KEY          - Public verification key
#   mms_eng_test_aws_access_key       - AWS access key (CI only)
#   mms_eng_test_aws_secret           - AWS secret key (CI only)
#   mms_eng_test_aws_region           - AWS region override (CI only)
#
set -Eeou pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/nix-cache.env"

OS_TYPE="$(uname -s)"
NIX_CONF_DIR="/etc/nix"
NIX_CUSTOM_CONF_PATH="${NIX_CONF_DIR}/nix.custom.conf"
NIX_CONF_PATH="${NIX_CONF_DIR}/nix.conf"

# --- Ensure Nix is on PATH ---
# When Nix was just installed in a previous CI step, it may not yet be on PATH.
if ! command -v nix &>/dev/null; then
    if [[ -f "/nix/var/nix/profiles/default/etc/profile.d/nix-daemon.sh" ]]; then
        # shellcheck disable=SC1091
        . "/nix/var/nix/profiles/default/etc/profile.d/nix-daemon.sh"
    fi
fi

if ! command -v nix &>/dev/null; then
    echo "ERROR: Nix is not installed."
    echo "  Run ./scripts/devbox/install-devbox.sh first."
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

# ============================================================================
# Step 1: AWS credentials
# ============================================================================

echo "=== Step 1/3: AWS credentials ==="

if [[ -n "${mms_eng_test_aws_access_key:-}" ]] && [[ -n "${mms_eng_test_aws_secret:-}" ]]; then
    # CI environment — write credentials from Evergreen expansion variables.
    AWS_REGION="${mms_eng_test_aws_region:-us-east-1}"

    echo "  CI detected (mms_eng_test_aws_access_key is set)."
    mkdir -p ~/.aws

    printf '[default]\naws_access_key_id = %s\naws_secret_access_key = %s\n' \
        "${mms_eng_test_aws_access_key}" "${mms_eng_test_aws_secret}" \
        > ~/.aws/credentials

    printf '[default]\nregion = %s\n' "${AWS_REGION}" \
        > ~/.aws/config

    chmod 600 ~/.aws/credentials ~/.aws/config
    echo "  Wrote ~/.aws/credentials"
else
    echo "  Local dev (no CI expansion variables)."
    if [[ -f "${HOME}/.aws/credentials" ]]; then
        echo "  Found ~/.aws/credentials"
    else
        echo "  WARNING: ~/.aws/credentials not found."
        echo "  Run 'aws sso login' or configure ~/.aws/credentials for S3 access."
    fi
fi

# The Nix daemon runs as root and reads root's ~/.aws/, not the current user's.
# Copy credentials so the daemon can authenticate to S3.
#   macOS:  root home is /var/root
#   Linux:  root home is /root
if [[ -f "${HOME}/.aws/credentials" ]]; then
    if [[ "${OS_TYPE}" == "Darwin" ]]; then
        ROOT_AWS_DIR="/var/root/.aws"
    else
        ROOT_AWS_DIR="/root/.aws"
    fi
    echo "  Copying credentials to ${ROOT_AWS_DIR}/ (Nix daemon runs as root)..."
    sudo mkdir -p "${ROOT_AWS_DIR}"
    sudo cp "${HOME}/.aws/credentials" "${ROOT_AWS_DIR}/credentials"
    sudo cp "${HOME}/.aws/config" "${ROOT_AWS_DIR}/config" 2>/dev/null || true
    sudo chmod 600 "${ROOT_AWS_DIR}/credentials"
    sudo chmod 600 "${ROOT_AWS_DIR}/config" 2>/dev/null || true
    echo "  Done."
fi

echo ""

# ============================================================================
# Step 2: Nix cache configuration
# ============================================================================

echo "=== Step 2/3: Nix cache configuration ==="
echo "  Config:  ${NIX_CUSTOM_CONF_PATH}"
echo "  Bucket:  s3://${MCK_NIX_CACHE_BUCKET} (${MCK_NIX_CACHE_REGION})"

sudo mkdir -p "${NIX_CONF_DIR}"

S3_SUBSTITUTER="s3://${MCK_NIX_CACHE_BUCKET}?region=${MCK_NIX_CACHE_REGION}&scheme=https&priority=10"
NIXOS_SUBSTITUTER="https://cache.nixos.org?priority=40"

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

# Ensure nix.conf includes nix.custom.conf
INCLUDE_DIRECTIVE="!include nix.custom.conf"

if [[ ! -f "${NIX_CONF_PATH}" ]]; then
    echo "  Creating ${NIX_CONF_PATH} with include directive."
    write_file "${NIX_CONF_PATH}" "${INCLUDE_DIRECTIVE}"
elif ! grep -qF "${INCLUDE_DIRECTIVE}" "${NIX_CONF_PATH}" 2>/dev/null; then
    echo "  Adding include directive to ${NIX_CONF_PATH}"
    _tmp_file="$(mktemp)"
    sudo cp "${NIX_CONF_PATH}" "${_tmp_file}"
    {
        echo ""
        echo "# Include MCK S3 cache configuration"
        echo "${INCLUDE_DIRECTIVE}"
    } >> "${_tmp_file}"
    sudo cp "${_tmp_file}" "${NIX_CONF_PATH}"
    sudo chmod 644 "${NIX_CONF_PATH}"
    rm -f "${_tmp_file}"
else
    echo "  ${NIX_CONF_PATH} already includes nix.custom.conf"
fi

echo ""

# ============================================================================
# Step 3: Restart Nix daemon
# ============================================================================

echo "=== Step 3/3: Restarting Nix daemon ==="

if [[ "${OS_TYPE}" == "Darwin" ]]; then
    if sudo launchctl kickstart -k system/systems.determinate.nix-daemon 2>/dev/null; then
        echo "  Restarted (Determinate Systems daemon)."
    elif sudo launchctl kickstart -k system/org.nixos.nix-daemon 2>/dev/null; then
        echo "  Restarted (NixOS daemon)."
    else
        echo "  WARNING: Could not restart daemon. Restart manually:"
        echo "    sudo launchctl kickstart -k system/systems.determinate.nix-daemon"
    fi
elif [[ "${OS_TYPE}" == "Linux" ]]; then
    if sudo systemctl restart nix-daemon 2>/dev/null; then
        echo "  Restarted (systemd)."
    else
        echo "  nix-daemon not managed by systemd (single-user Nix assumed)."
    fi
fi

echo ""
echo "=== Nix S3 cache configured ==="
echo ""
echo "Cache resolution order:"
echo "  1. /nix/store (local)"
echo "  2. s3://${MCK_NIX_CACHE_BUCKET} (team cache, priority=10)"
echo "  3. cache.nixos.org (public, priority=40)"
