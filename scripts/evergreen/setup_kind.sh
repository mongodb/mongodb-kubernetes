#!/usr/bin/env bash
set -Eeou pipefail

source scripts/dev/set_env_context.sh

# Store the lowercase name of Operating System
os=$(uname | tr '[:upper:]' '[:lower:]')
# Detect architecture
arch=$(uname -m)
case ${arch} in
    x86_64) arch_suffix="amd64" ;;
    aarch64|arm64) arch_suffix="arm64" ;;
    *) echo "Unsupported architecture: ${arch}" >&2; exit 1 ;;
esac
# This should be changed when needed
latest_version="v0.27.0"

mkdir -p "${PROJECT_DIR}/bin/"
echo "Saving kind to ${PROJECT_DIR}/bin"
curl --retry 3 --silent -L "https://github.com/kubernetes-sigs/kind/releases/download/${latest_version}/kind-${os}-${arch_suffix}" -o kind

chmod +x kind
sudo mv kind "${PROJECT_DIR}/bin"
echo "Installed kind in ${PROJECT_DIR}/bin"
