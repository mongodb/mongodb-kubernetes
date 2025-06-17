#!/usr/bin/env bash
set -Eeou pipefail -o posix

source scripts/dev/set_env_context.sh

# Store the lowercase name of Operating System
os=$(uname | tr '[:upper:]' '[:lower:]')
# This should be changed when needed
latest_version="v0.27.0"

mkdir -p "${PROJECT_DIR}/bin/"
echo "Saving kind to ${PROJECT_DIR}/bin"
curl --retry 3 --silent -L "https://github.com/kubernetes-sigs/kind/releases/download/${latest_version}/kind-${os}-amd64" -o kind

chmod +x kind
sudo mv kind "${PROJECT_DIR}/bin"
echo "Installed kind in ${PROJECT_DIR}/bin"
