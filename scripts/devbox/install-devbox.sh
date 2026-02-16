#!/usr/bin/env bash
# install-devbox.sh
#
# Install Nix and Devbox on macOS or Linux (Ubuntu).
# Idempotent - safe to run multiple times.
#
# This script:
#   1. Installs Nix using the Determinate Systems installer (multi-user mode)
#   2. Installs Devbox CLI from Jetify
#   3. Optionally configures the S3 binary cache
#
# Usage:
#   ./scripts/devbox/install-devbox.sh [--with-cache]
#
# Options:
#   --with-cache   Also configure the S3 Nix binary cache after installation
#
# Supported platforms:
#   - macOS (aarch64, x86_64) - Apple Silicon and Intel
#   - Linux (x86_64)          - Ubuntu on Evergreen CI hosts
#
set -Eeou pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/nix-cache.env"

WITH_CACHE=false
if [[ "${1:-}" == "--with-cache" ]]; then
    WITH_CACHE=true
fi

OS="$(uname -s)"
ARCH="$(uname -m)"

echo "=== MCK Devbox Installation ==="
echo "  OS:   ${OS} (${ARCH})"
echo ""

# --- Step 1: Install Nix ---

echo "Step 1/3: Installing Nix..."

if command -v nix &>/dev/null; then
    NIX_VERSION=$(nix --version 2>/dev/null || echo "unknown")
    echo "  Nix already installed: ${NIX_VERSION}"
else
    echo "  Installing Nix via Determinate Systems installer..."
    echo "  This installs Nix in multi-user mode with flakes enabled."
    echo ""

    # The Determinate Systems installer:
    # - Works on macOS and Linux
    # - Enables flakes and nix-command by default
    # - Sets up the nix-daemon (multi-user mode)
    # - Creates /nix/store with proper permissions
    # - Is idempotent
    curl --proto '=https' --tlsv1.2 -sSf -L "${NIX_INSTALLER_URL}" | \
        sh -s -- install --no-confirm

    # Source the nix profile so nix is available in this shell
    if [[ -f "/nix/var/nix/profiles/default/etc/profile.d/nix-daemon.sh" ]]; then
        # shellcheck disable=SC1091
        . "/nix/var/nix/profiles/default/etc/profile.d/nix-daemon.sh"
    elif [[ -f "${HOME}/.nix-profile/etc/profile.d/nix.sh" ]]; then
        # shellcheck disable=SC1091
        . "${HOME}/.nix-profile/etc/profile.d/nix.sh"
    fi

    echo ""
    echo "  Nix installed: $(nix --version)"
fi

echo ""

# --- Step 2: Install Devbox ---

echo "Step 2/3: Installing Devbox..."

if command -v devbox &>/dev/null; then
    DEVBOX_CURRENT=$(devbox version 2>/dev/null || echo "unknown")
    echo "  Devbox already installed: ${DEVBOX_CURRENT}"
else
    echo "  Installing Devbox CLI..."

    curl -fsSL https://get.jetify.com/devbox | bash -s -- -f

    echo ""
    echo "  Devbox installed: $(devbox version)"
fi

echo ""

# --- Step 3: Verify installation ---

echo "Step 3/3: Verifying installation..."

ERRORS=0

for cmd in nix nix-store devbox; do
    if command -v "${cmd}" &>/dev/null; then
        echo "  ${cmd}: OK"
    else
        echo "  ${cmd}: MISSING"
        ERRORS=$((ERRORS + 1))
    fi
done

# Verify nix experimental features (flakes must be enabled)
if nix flake --help &>/dev/null; then
    echo "  nix flakes: enabled"
else
    echo "  nix flakes: NOT enabled (may need nix.conf update)"
    echo "  Add to nix.conf: experimental-features = nix-command flakes"
fi

echo ""

if [[ ${ERRORS} -gt 0 ]]; then
    echo "ERROR: ${ERRORS} tool(s) not found. Installation may have failed."
    echo "       You may need to restart your shell or source the nix profile:"
    echo "         . /nix/var/nix/profiles/default/etc/profile.d/nix-daemon.sh"
    exit 1
fi

# --- Optional: Configure S3 cache ---

if [[ "${WITH_CACHE}" == "true" ]]; then
    echo "Configuring S3 Nix binary cache..."
    "${SCRIPT_DIR}/configure-nix-cache.sh"
fi

echo ""
echo "=== Installation complete ==="
echo ""
echo "Quick start:"
echo "  cd $(pwd)"
echo "  devbox shell"
echo ""
echo "To also configure the S3 cache:"
echo "  ./scripts/devbox/configure-nix-cache.sh"
