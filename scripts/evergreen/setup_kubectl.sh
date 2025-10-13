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

kubectl_version=$(curl --retry 5 -Ls https://dl.k8s.io/release/stable.txt)
echo "Downloading kubectl ${kubectl_version} for ${ARCH}"

curl --retry 5 -LOs "https://dl.k8s.io/release/${kubectl_version}/bin/linux/${ARCH}/kubectl"
chmod +x kubectl
echo "kubectl version --client"
./kubectl version --client
mv kubectl "${bindir}"

echo "Downloading helm for ${ARCH}"
helm_archive="${tmpdir}/helm.tgz"
helm_version="v3.17.1"
curl -s https://get.helm.sh/helm-${helm_version}-linux-"${ARCH}".tar.gz --output "${helm_archive}"

tar xfz "${helm_archive}" -C "${tmpdir}" &> /dev/null
mv "${tmpdir}/linux-${ARCH}/helm" "${bindir}"
"${bindir}"/helm version
