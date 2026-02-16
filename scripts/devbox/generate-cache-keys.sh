#!/usr/bin/env bash
# generate-cache-keys.sh
#
# One-time setup: Generate a Nix binary cache signing keypair.
# The private key is used to sign packages before pushing to S3.
# The public key is distributed to all consumers (developers, CI) so they can
# verify packages pulled from the cache.
#
# IMPORTANT: The private key must be kept secret. Store it in your secrets vault
# (e.g., Evergreen project secrets), NOT in version control.
#
# Usage:
#   ./scripts/devbox/generate-cache-keys.sh [key-name] [output-dir]
#
# Example:
#   ./scripts/devbox/generate-cache-keys.sh mck-nix-cache-1 /tmp/nix-keys
#
# Outputs:
#   <output-dir>/nix-cache-priv-key.pem   - Private signing key (KEEP SECRET)
#   <output-dir>/nix-cache-pub-key.pem    - Public verification key (distribute)
#
set -Eeou pipefail

KEY_NAME="${1:-mck-nix-cache-1}"
OUTPUT_DIR="${2:-.}"

PRIV_KEY_PATH="${OUTPUT_DIR}/nix-cache-priv-key.pem"
PUB_KEY_PATH="${OUTPUT_DIR}/nix-cache-pub-key.pem"

# --- Pre-checks ---

if ! command -v nix-store &>/dev/null; then
    echo "ERROR: nix-store not found. Install Nix first:"
    echo "  curl --proto '=https' --tlsv1.2 -sSf -L https://install.determinate.systems/nix | sh -s -- install"
    exit 1
fi

if [[ -f "${PRIV_KEY_PATH}" ]]; then
    echo "ERROR: Private key already exists at ${PRIV_KEY_PATH}"
    echo "       Remove it first if you want to regenerate."
    exit 1
fi

mkdir -p "${OUTPUT_DIR}"

# --- Generate the keypair ---

echo "Generating Nix binary cache signing keypair..."
echo "  Key name: ${KEY_NAME}"

nix-store --generate-binary-cache-key \
    "${KEY_NAME}" \
    "${PRIV_KEY_PATH}" \
    "${PUB_KEY_PATH}"

chmod 600 "${PRIV_KEY_PATH}"
chmod 644 "${PUB_KEY_PATH}"

echo ""
echo "=== Keys generated successfully ==="
echo ""
echo "Private key (KEEP SECRET - do NOT commit to git):"
echo "  ${PRIV_KEY_PATH}"
echo ""
echo "Public key (safe to distribute):"
echo "  ${PUB_KEY_PATH}"
echo ""
echo "Public key value (add this to nix.conf and CI config):"
cat "${PUB_KEY_PATH}"
echo ""
echo ""
echo "=== Next steps ==="
echo "1. Store the private key in your secrets vault (e.g., Evergreen project secrets)."
echo "   For CI, configure an Evergreen expansion variable 'nix_cache_signing_key'"
echo "   with the contents of: ${PRIV_KEY_PATH}"
echo ""
echo "2. Update MCK_NIX_CACHE_PUBLIC_KEY in scripts/devbox/nix-cache.env"
echo "   with the public key value above."
echo ""
echo "3. To push cache locally, export the key and run push-cache.sh:"
echo "   export MCK_NIX_CACHE_PRIV_KEY=\"\$(cat ${PRIV_KEY_PATH})\""
echo "   ./scripts/devbox/push-cache.sh"
echo ""
echo "4. Delete the local private key copy once stored in your vault:"
echo "   rm ${PRIV_KEY_PATH}"
