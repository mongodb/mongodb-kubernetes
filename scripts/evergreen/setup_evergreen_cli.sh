#!/usr/bin/env bash
#
# Installs the evergreen CLI into ${PROJECT_DIR}/bin so the
# validate_evergreen_config pre-commit gate can run `evergreen validate`.
#
# The evergreen CLI is not baked into buildhost base images (KUBE-443), so
# CI's lint_repo runs this. The binary URL is resolved from the public Evergreen
# S3 static client manifest for the current platform/arch; the
# `evergreen.mongodb.com/clients/...` endpoint is auth-gated and returns 401.
#

set -Eeou pipefail

source scripts/dev/set_env_context.sh
source scripts/funcs/install

# Provision Evergreen client auth from CI service-account credentials (the same
# EVERGREEN_USER/EVERGREEN_API_KEY the notify_* functions use) so `evergreen
# validate` can run. Local dev has no such env vars, so this never overwrites a
# developer's ~/.evergreen.yml.
if [[ -n "${EVERGREEN_USER:-}" && -n "${EVERGREEN_API_KEY:-}" ]]; then
  cat > "${HOME}/.evergreen.yml" <<EOF
user: "${EVERGREEN_USER}"
api_key: "${EVERGREEN_API_KEY}"
api_server_host: "https://evergreen.mongodb.com/api"
ui_server_host: "https://evergreen.mongodb.com"
EOF
  echo "Wrote ${HOME}/.evergreen.yml from Evergreen service credentials"
fi

if command -v evergreen >/dev/null 2>&1; then
  echo "evergreen CLI already on PATH: $(command -v evergreen)"
  exit 0
fi

os="$(uname -s | tr '[:upper:]' '[:lower:]')"
arch="$(detect_architecture)"
manifest_url="https://evg-bucket-evergreen.s3.amazonaws.com/evergreen/clients/latest/manifest.json"

echo "Resolving evergreen CLI URL for ${os}/${arch} from ${manifest_url}"
manifest_file="$(mktemp)"
curl_with_retry -sS -L "${manifest_url}" -o "${manifest_file}"
url="$(python3 -c "import json,sys;d=json.load(sys.stdin);print(next(b['url'] for b in d['client_binaries'] if b['os']=='${os}' and b['arch']=='${arch}'))" < "${manifest_file}")"
rm -f "${manifest_file}"

bindir="${PROJECT_DIR}/bin"
mkdir -p "${bindir}"

echo "Downloading evergreen CLI from ${url}"
curl_with_retry -L "${url}" -o "${bindir}/evergreen"
chmod +x "${bindir}/evergreen"

"${bindir}/evergreen" version
echo "evergreen CLI installed to ${bindir}/evergreen"
