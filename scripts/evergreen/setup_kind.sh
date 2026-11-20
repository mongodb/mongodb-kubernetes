#!/usr/bin/env bash
set -Eeou pipefail

source scripts/dev/set_env_context.sh
source scripts/funcs/install

# Store the lowercase name of Operating System
os=$(uname | tr '[:upper:]' '[:lower:]')
# Detect architecture
arch_suffix=$(detect_architecture)
# This should be changed when needed
latest_version="v0.33.0"

fetch_kind() {
  mkdir -p "${PROJECT_DIR}/bin"
  echo "Saving kind to ${PROJECT_DIR}/bin"
  curl_with_retry -L "https://github.com/kubernetes-sigs/kind/releases/download/${latest_version}/kind-${os}-${arch_suffix}" -o "${PROJECT_DIR}/bin/kind"
  chmod +x "${PROJECT_DIR}/bin/kind"
  echo "Installed kind in ${PROJECT_DIR}/bin"
}

# Only proceed with installation if architecture is supported (amd64 or arm64)
if [[ "${arch_suffix}" == "amd64" || "${arch_suffix}" == "arm64" ]]; then
  ensure_binary kind fetch_kind
else
  echo "Architecture ${arch_suffix} not supported for kind installation, skipping"
fi
