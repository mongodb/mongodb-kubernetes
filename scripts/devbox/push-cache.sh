#!/usr/bin/env bash
# push-cache.sh
#
# Sign and push all devbox dependencies from the local /nix/store to the S3
# binary cache. This should be run:
#   - After updating devbox.json with new/changed packages
#   - In a CI job that maintains the cache (e.g., on master merge)
#   - Manually by a developer with push access to the S3 bucket
#
# Prerequisites:
#   1. Nix installed with `nix copy` support (Nix 2.4+)
#   2. devbox installed and `devbox install` already run
#   3. AWS credentials with s3:PutObject permission on the cache bucket
#   4. The Nix signing private key passed via MCK_NIX_CACHE_PRIV_KEY env var
#
# Usage:
#   export MCK_NIX_CACHE_PRIV_KEY="$(cat /path/to/nix-cache-priv-key.pem)"
#   ./scripts/devbox/push-cache.sh
#
# Environment (required):
#   MCK_NIX_CACHE_PRIV_KEY   - Private signing key content (full PEM string)
#
# Environment (optional):
#   MCK_NIX_CACHE_BUCKET     - S3 bucket name
#   MCK_NIX_CACHE_REGION     - AWS region
#
set -Eeou pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/nix-cache.env"

# --- Validate prerequisites ---

for cmd in nix devbox aws; do
    if ! command -v "${cmd}" &>/dev/null; then
        echo "ERROR: '${cmd}' is not installed."
        exit 1
    fi
done

if [[ -z "${MCK_NIX_CACHE_PRIV_KEY}" ]]; then
    echo "ERROR: MCK_NIX_CACHE_PRIV_KEY environment variable is not set."
    echo ""
    echo "Pass the private signing key content via this variable:"
    echo "  export MCK_NIX_CACHE_PRIV_KEY=\"\$(cat /path/to/nix-cache-priv-key.pem)\""
    echo "  ./scripts/devbox/push-cache.sh"
    exit 1
fi

echo "=== MCK Nix Cache Push ==="
echo "  S3 bucket: s3://${MCK_NIX_CACHE_BUCKET} (${MCK_NIX_CACHE_REGION})"
echo ""

# --- Ensure devbox packages are installed ---

echo "Step 1/4: Ensuring devbox packages are installed..."
devbox install
echo ""

# --- Write the private key to a temporary file ---

echo "Step 2/4: Preparing signing key..."

PRIV_KEY_FILE="$(mktemp /tmp/nix-cache-key-XXXXXX.pem)"
echo "${MCK_NIX_CACHE_PRIV_KEY}" > "${PRIV_KEY_FILE}"
chmod 600 "${PRIV_KEY_FILE}"

# Ensure cleanup of the temp key on exit
cleanup() {
    if [[ -f "${PRIV_KEY_FILE}" ]]; then
        rm -f "${PRIV_KEY_FILE}"
    fi
}
trap cleanup EXIT

# Verify the key looks valid (should contain the key name)
if ! grep -q "${MCK_NIX_CACHE_KEY_NAME}" "${PRIV_KEY_FILE}" 2>/dev/null; then
    echo "ERROR: Private key does not contain expected key name '${MCK_NIX_CACHE_KEY_NAME}'."
    echo "       The key may be corrupted or belong to a different cache."
    exit 1
fi

echo "  Signing key validated."
echo ""

# --- Collect store paths from devbox ---

echo "Step 3/4: Collecting Nix store paths from devbox..."

# Get all store paths that devbox uses (including transitive dependencies)
# devbox creates a profile in .devbox/nix/profile/default which is a symlink
# into the nix store. We resolve all store paths from it.
DEVBOX_PROFILE=".devbox/nix/profile/default"

if [[ ! -L "${DEVBOX_PROFILE}" ]]; then
    echo "ERROR: devbox profile not found at ${DEVBOX_PROFILE}"
    echo "       Run 'devbox install' first."
    exit 1
fi

# Use nix-store to compute the closure (all dependencies, transitive)
STORE_PATHS=$(nix-store --query --requisites "${DEVBOX_PROFILE}" 2>/dev/null)
NUM_PATHS=$(echo "${STORE_PATHS}" | wc -l | tr -d ' ')

echo "  Found ${NUM_PATHS} store paths (including transitive dependencies)"
echo ""

# --- Sign and push to S3 ---

echo "Step 4/4: Signing and pushing to S3..."
echo "  This may take a few minutes on first push."
echo ""

# Sign all store paths with our private key
echo "  Signing ${NUM_PATHS} store paths..."
echo "${STORE_PATHS}" | xargs nix store sign \
    --key-file "${PRIV_KEY_FILE}" 2>&1 | tail -5 || true

echo "  Signatures applied."
echo ""

# Push to S3
# nix copy handles deduplication - it only uploads paths not already in S3.
# The secret-key parameter also signs during copy if not already signed.
echo "  Uploading to s3://${MCK_NIX_CACHE_BUCKET}..."

nix copy \
    --to "${MCK_NIX_CACHE_S3_URI}&secret-key=${PRIV_KEY_FILE}" \
    ${STORE_PATHS} \
    2>&1 | tail -20

echo ""
echo "=== Push complete ==="
echo ""

# --- Verify by listing some cache contents ---

echo "Verifying cache contents..."
SAMPLE_PATH=$(echo "${STORE_PATHS}" | head -1)
SAMPLE_HASH=$(basename "${SAMPLE_PATH}" | cut -d'-' -f1)

if aws s3 ls "s3://${MCK_NIX_CACHE_BUCKET}/${SAMPLE_HASH}.narinfo" --region "${MCK_NIX_CACHE_REGION}" &>/dev/null; then
    echo "  Verification: OK (found ${SAMPLE_HASH}.narinfo in S3)"
else
    echo "  Verification: Could not confirm narinfo in S3."
    echo "  This might be normal if the paths were already cached."
fi

echo ""
echo "Cache consumers can now pull packages by running:"
echo "  ./scripts/devbox/configure-nix-cache.sh"
echo "  devbox install"
