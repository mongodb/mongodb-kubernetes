#!/usr/bin/env bash
set -Eeou pipefail

source scripts/dev/set_env_context.sh
source scripts/funcs/install

# Detect the current architecture
ARCH=$(detect_architecture standard)
echo "Detected architecture: ${ARCH}"

bindir="${PROJECT_DIR}/bin"
tmpdir="${PROJECT_DIR}/tmp"
mkdir -p "${bindir}" "${tmpdir}"

fetch_kubectl() {
  pushd "${tmpdir}" > /dev/null
  download_kubectl_binary "${KUBECTL_VERSION}" "${ARCH}"
  ./kubectl version --client
  mv kubectl "${bindir}"
  popd > /dev/null
}

fetch_helm() {
  pushd "${tmpdir}" > /dev/null
  download_helm_binary "${HELM_VERSION}" "${ARCH}"
  mv "linux-${ARCH}/helm" "${bindir}"
  rm -rf "linux-${ARCH}/"
  popd > /dev/null
}

# download_kube_tools, setup_minikube_host and setup_evg_host all call this script.
# ensure_binary makes every call after the first one free.
ensure_binary kubectl fetch_kubectl
ensure_binary helm fetch_helm

"${bindir}"/kubectl version --client
"${bindir}"/helm version
