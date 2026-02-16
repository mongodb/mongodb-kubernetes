#!/usr/bin/env bash
# setup-ci-cache.sh
#
# Full Evergreen CI setup: install Nix + Devbox, configure S3 cache, install
# devbox packages. Designed to replace the individual setup_kubectl.sh,
# setup_kind.sh, etc. scripts with a single devbox-based installation.
#
# This script is idempotent and optimized for Evergreen's ephemeral EC2 hosts:
#   - Installs Nix + Devbox if not already present
#   - Configures S3-based binary cache (same region = zero egress cost)
#   - Runs `devbox install` to populate all tools from cache
#   - Exports PATH so devbox-managed binaries are available to subsequent steps
#
# Evergreen integration:
#   Add to .evergreen-functions.yml:
#     setup_devbox: &setup_devbox
#       command: subprocess.exec
#       type: setup
#       retry_on_failure: true
#       params:
#         working_dir: src/github.com/mongodb/mongodb-kubernetes
#         binary: scripts/devbox/setup-ci-cache.sh
#
# Environment variables (set by Evergreen or IAM):
#   AWS credentials are obtained via EC2 instance profile (IAM role).
#   No explicit AWS_ACCESS_KEY_ID needed if the Evergreen worker has the
#   correct IAM role with s3:GetObject permission on the cache bucket.
#
# Optional environment overrides:
#   MCK_NIX_CACHE_BUCKET   - S3 bucket name
#   MCK_NIX_CACHE_REGION   - AWS region
#   DEVBOX_USE_CACHE       - Set to "false" to skip S3 cache configuration
#   DEVBOX_INSTALL_ONLY    - Set to "true" to only install devbox, not packages
#
set -Eeou pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/nix-cache.env"

DEVBOX_USE_CACHE="${DEVBOX_USE_CACHE:-true}"
DEVBOX_INSTALL_ONLY="${DEVBOX_INSTALL_ONLY:-false}"

echo "============================================"
echo "  MCK Devbox CI Setup"
echo "============================================"
echo "  Host:       $(hostname)"
echo "  OS:         $(uname -s) $(uname -r)"
echo "  Arch:       $(uname -m)"
echo "  Use cache:  ${DEVBOX_USE_CACHE}"
echo "  Bucket:     s3://${MCK_NIX_CACHE_BUCKET} (${MCK_NIX_CACHE_REGION})"
echo "============================================"
echo ""

START_TIME=$(date +%s)

# --- Step 1: Install Nix ---

echo ">>> Step 1/5: Nix installation"

if command -v nix &>/dev/null; then
    echo "    Nix already installed: $(nix --version)"
else
    echo "    Installing Nix..."

    # Use Determinate Systems installer for CI
    # --no-confirm: non-interactive
    # --extra-conf: enable flakes and nix-command
    curl --proto '=https' --tlsv1.2 -sSf -L "${NIX_INSTALLER_URL}" | \
        sh -s -- install linux --no-confirm \
            --init none \
            --extra-conf "experimental-features = nix-command flakes"

    # Source nix for this shell session
    if [[ -f "/nix/var/nix/profiles/default/etc/profile.d/nix-daemon.sh" ]]; then
        # shellcheck disable=SC1091
        . "/nix/var/nix/profiles/default/etc/profile.d/nix-daemon.sh"
    fi

    echo "    Nix installed: $(nix --version)"
fi

echo ""

# --- Step 2: Install Devbox ---

echo ">>> Step 2/5: Devbox installation"

if command -v devbox &>/dev/null; then
    echo "    Devbox already installed: $(devbox version)"
else
    echo "    Installing Devbox..."
    curl -fsSL https://get.jetify.com/devbox | bash -s -- -f
    echo "    Devbox installed: $(devbox version)"
fi

echo ""

# --- Step 3: Configure S3 Cache ---

echo ">>> Step 3/5: S3 cache configuration"

if [[ "${DEVBOX_USE_CACHE}" == "true" ]]; then
    # Writes /etc/nix/nix.custom.conf with S3 cache as primary substituter,
    # restarts the Nix daemon, and cleans stale cache DB entries.
    "${SCRIPT_DIR}/configure-nix-cache.sh" 2>&1 | sed 's/^/    /'
else
    echo "    Skipped (DEVBOX_USE_CACHE=false)"
fi

echo ""

# --- Step 4: Install devbox packages ---

echo ">>> Step 4/5: Installing devbox packages"

if [[ "${DEVBOX_INSTALL_ONLY}" == "true" ]]; then
    echo "    Skipped (DEVBOX_INSTALL_ONLY=true)"
else
    # Navigate to the project root (where devbox.json lives)
    PROJECT_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
    cd "${PROJECT_ROOT}"

    echo "    Running devbox install in ${PROJECT_ROOT}..."
    echo "    (Packages will be pulled from S3 cache if available)"
    echo ""

    devbox install 2>&1 | sed 's/^/    /'

    echo ""
    echo "    Packages installed successfully."
fi

echo ""

# --- Step 5: Export environment ---

echo ">>> Step 5/5: Environment setup"

# Generate a shell snippet that Evergreen can source to get devbox tools on PATH
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
ENV_FILE="${PROJECT_ROOT}/.devbox-env.sh"

cd "${PROJECT_ROOT}"

# Use devbox to print the environment variables it would set
# This includes PATH modifications pointing to /nix/store binaries
devbox shellenv > "${ENV_FILE}" 2>/dev/null || true

if [[ -s "${ENV_FILE}" ]]; then
    echo "    Environment file written: ${ENV_FILE}"
    echo "    Source it in subsequent Evergreen steps:"
    echo "      source ${ENV_FILE}"
    echo ""

    # Also source it now so this script's caller gets the env
    # shellcheck disable=SC1090
    . "${ENV_FILE}"

    # Verify key tools are available
    echo "    Tool verification:"
    for tool in go python3 kubectl helm kind shellcheck; do
        if command -v "${tool}" &>/dev/null; then
            VERSION=$("${tool}" version 2>/dev/null | head -1 || "${tool}" --version 2>/dev/null | head -1 || echo "present")
            echo "      ${tool}: ${VERSION}"
        else
            echo "      ${tool}: NOT FOUND (may not be in devbox.json)"
        fi
    done
else
    echo "    WARNING: Could not generate devbox shellenv."
    echo "    Tools may need to be accessed via 'devbox run' instead."
fi

echo ""

# --- Summary ---

END_TIME=$(date +%s)
ELAPSED=$((END_TIME - START_TIME))

echo "============================================"
echo "  Setup complete in ${ELAPSED}s"
echo "============================================"
echo ""
echo "Devbox tools are now available. In Evergreen steps, source the env:"
echo "  source .devbox-env.sh"
echo ""
echo "Or run commands through devbox:"
echo "  devbox run -- kubectl version --client"
echo "  devbox run -- helm version"
