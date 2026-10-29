#!/usr/bin/env bash
#
# Disclaimer: this file was mostly generated with AI, as part of a POC.
#
# IdP-federation POC: Mac-side setup (runs on your laptop, where the operator
# runs in your IDE with LOCAL_OPERATOR=true).
#
#   scripts/dev/idp_poc_local_setup.sh
#
# What it does:
#   1. Builds the exec credential helper (bin/idp-credential) if missing.
#   2. Backs up your operator member kubeconfig ($KUBE_CONFIG_PATH) and rewrites
#      each member cluster's user entry from a static `token:` to an `exec:`
#      credential plugin pointing at the helper. Central is left untouched.
#
# Prereq: the kubeconfig must already exist. Generate it with your normal
# multi-cluster prep first (`make prepare-local-e2e` from the Mac), which runs
# the plugin and writes multicluster_kubeconfig to $KUBE_CONFIG_PATH.
#
# The POC identity values MUST match scripts/dev/idp_poc_host_setup.sh.
#
set -Eeou pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(cd "${script_dir}/../.." && pwd)"

# --- POC identity (must match the host-side script) ------------------------
IDP_NAME="${IDP_NAME:-central-idp}"
IDP_PORT="${IDP_PORT:-4200}"
IDP_ISSUER="${IDP_ISSUER:-https://${IDP_NAME}:${IDP_PORT}/default}"
OIDC_CLIENT_ID="${OIDC_CLIENT_ID:-mck-operator}"
SCOPE="${SCOPE:-${OIDC_CLIENT_ID}}" # helper's scope becomes the token aud == member --oidc-client-id
read -ra MEMBERS <<< "${MEMBERS:-kind-e2e-cluster-1 kind-e2e-cluster-2 kind-e2e-cluster-3}"

KC="${KUBE_CONFIG_PATH:-${HOME}/.operator-dev/multicluster_kubeconfig}"
HELPER="${PROJECT_DIR}/bin/idp-credential"
TOKEN_URL="${IDP_ISSUER}/token"

echo "== IdP POC local setup =="
echo "kubeconfig: ${KC}"
echo "helper:     ${HELPER}"
echo "issuer:     ${IDP_ISSUER}  client_id/scope: ${OIDC_CLIENT_ID}"

if [[ ! -f "${KC}" ]]; then
  echo "ERROR: ${KC} not found." >&2
  echo "Generate it first with your normal multi-cluster prep" >&2
  echo "(make prepare-local-e2e from the Mac)." >&2
  exit 1
fi

if ! command -v yq >/dev/null 2>&1; then
  echo "ERROR: yq (mikefarah) is required but not on PATH." >&2
  exit 1
fi

# Build the helper if needed. It is stdlib-only; -mod=mod bypasses the repo's
# pre-existing vendor/go.mod drift without touching the vendor tree.
if [[ ! -x "${HELPER}" ]]; then
  echo "Building helper..."
  (cd "${PROJECT_DIR}" && go build -mod=mod -o "${HELPER}" ./cmd/idp-credential/)
fi

# exec credential plugin entry. The client secret is a dummy because the mock
# IdP accepts any value; production reads it from a mounted file instead.
EXEC='{"exec": {"apiVersion": "client.authentication.k8s.io/v1", "command": "'"$HELPER"'", "args": ["--token-url='"$TOKEN_URL"'", "--client-id='"$OIDC_CLIENT_ID"'", "--client-secret=dummy", "--scope='"$SCOPE"'", "--insecure-skip-verify"], "interactiveMode": "Never"}}'

backup="${KC}.bak-$(date +%Y%m%d%H%M%S)"
cp "${KC}" "${backup}"
echo "Backed up kubeconfig to ${backup}"

for m in "${MEMBERS[@]}"; do
  yq -i '(.users[] | select(.name == "'"$m"'") | .user) = '"$EXEC"'' "${KC}"
  echo "  swapped ${m} -> exec"
done

echo
echo "Verifying (expect: exec=true x${#MEMBERS[@]}, token=false x${#MEMBERS[@]})..."
echo "has(exec):  $(yq '.users[].user | has("exec")' "${KC}" | tr '\n' ' ')"
echo "has(token): $(yq '.users[].user | has("token")' "${KC}" | tr '\n' ' ')"

echo
echo "== Local setup complete =="
echo "Remaining one-time steps:"
echo "  1. sudo sh -c 'echo \"127.0.0.1 ${IDP_NAME}\" >> /etc/hosts'   # helper's Host header must match the issuer"
echo "  2. Keep the member API servers tunneled (your normal):   scripts/dev/evg_host.sh tunnel"
echo "  3. In a SECOND terminal, forward the IdP port (reuses evg_host.sh's existing 'ssh' subcommand; no tunnel change):"
echo "       scripts/dev/evg_host.sh ssh -N -L ${IDP_PORT}:127.0.0.1:${IDP_PORT}"
echo "  4. Run the operator in your IDE ('Operator multi-cluster'), then create a MongoDBMultiCluster CR."
