#!/usr/bin/env bash
set -Eeou pipefail

source scripts/dev/set_env_context.sh

if command -v kind &>/dev/null; then
    echo "Kind is already present in this host"
    kind version

    exit 0
fi

# Store the lowercase name of Operating System
os=$(uname | tr '[:upper:]' '[:lower:]')
# This should be changed when needed
latest_version="v0.22.0"

mkdir -p "${workdir:?}/bin/"
echo "Saving kind to ${workdir}/bin"
curl --retry 3 --silent -L "https://github.com/kubernetes-sigs/kind/releases/download/${latest_version}/kind-${os}-amd64" -o kind

chmod +x kind
mv kind "${workdir}/bin"
echo "Installed kind in ${workdir}/bin"
