#!/usr/bin/env bash
set -Eeou pipefail

source scripts/dev/set_env_context.sh
source scripts/funcs/install

# Store the lowercase name of Operating System
os=$(uname | tr '[:upper:]' '[:lower:]')
# Detect architecture
arch_suffix=$(detect_architecture)
# This should be changed when needed
latest_version="v0.31.0"

# Only proceed with installation if architecture is supported (amd64 or arm64)
if [[ "${arch_suffix}" == "amd64" || "${arch_suffix}" == "arm64" ]]; then
  mkdir -p "${1:-${PROJECT_DIR}}/bin"
  echo "Saving kind to ${1:-${PROJECT_DIR}}/bin"
  curl_with_retry -L "https://github.com/kubernetes-sigs/kind/releases/download/${latest_version}/kind-${os}-${arch_suffix}" -o kind

  chmod +x kind
  sudo mv kind "${1:-${PROJECT_DIR}}/bin"
  echo "Installed kind in ${1:-${PROJECT_DIR}}/bin"
else
  echo "Architecture ${arch_suffix} not supported for kind installation, skipping"
fi
