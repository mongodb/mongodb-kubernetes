#!/usr/bin/env bash
set -Eeou pipefail

source scripts/dev/set_env_context.sh

bindir="${PROJECT_DIR}/bin"
tmpdir="${PROJECT_DIR}/tmp"
mkdir -p "${bindir}" "${tmpdir}"

echo "Downloading latest kubectl"
curl -s --retry 3 -LO "https://dl.k8s.io/release/$(curl -L -s https://dl.k8s.io/release/stable.txt)/bin/linux/amd64/kubectl"
chmod +x kubectl
echo "kubectl version --client"
./kubectl version --client
mv kubectl "${bindir}"

echo "Downloading helm"
helm_archive="${tmpdir}/helm.tgz"
helm_version="v3.17.1"
curl -s https://get.helm.sh/helm-${helm_version}-linux-amd64.tar.gz --output "${helm_archive}"

tar xfz "${helm_archive}" -C "${tmpdir}" &> /dev/null
mv "${tmpdir}/linux-amd64/helm" "${bindir}"
"${bindir}"/helm version
