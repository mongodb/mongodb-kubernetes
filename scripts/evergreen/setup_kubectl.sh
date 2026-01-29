#!/usr/bin/env bash
set -Eeou pipefail

source scripts/dev/set_env_context.sh
source scripts/funcs/install

# Detect the current architecture
ARCH=$(detect_architecture)
echo "Detected architecture: ${ARCH}"

bindir="${PROJECT_DIR}/bin"
tmpdir="${PROJECT_DIR}/tmp"
mkdir -p "${bindir}" "${tmpdir}"

# Use pinned version from root-context (no external API call needed)
download_kubectl_binary "${KUBECTL_VERSION}" "${ARCH}"
echo "kubectl version --client"
./kubectl version --client
mv kubectl "${bindir}"

pushd "${tmpdir}" > /dev/null
download_helm_binary "${HELM_VERSION}" "${ARCH}"
mv linux-${ARCH}/helm "${bindir}"
rm -rf linux-${ARCH}/
popd > /dev/null

"${bindir}"/helm version
