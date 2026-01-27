#!/usr/bin/env bash
set -Eeou pipefail

source scripts/dev/set_env_context.sh

bindir="${PROJECT_DIR}/bin"
tmpdir="${PROJECT_DIR}/tmp"
mkdir -p "${bindir}" "${tmpdir}"

echo "Downloading latest kubectl"
kubectl_version=$(curl --retry 10 --retry-delay 10 --retry-all-errors --max-time 60 -fsSL https://dl.k8s.io/release/stable.txt)
curl --retry 10 --retry-delay 10 --retry-all-errors --max-time 300 -fsSLO "https://dl.k8s.io/release/${kubectl_version}/bin/linux/amd64/kubectl"
chmod +x kubectl
echo "kubectl version --client"
./kubectl version --client
mv kubectl "${bindir}"

echo "Downloading helm"
helm_archive="${tmpdir}/helm.tgz"
helm_version="v3.17.1"
curl --retry 10 --retry-delay 10 --retry-all-errors --max-time 300 -fsSL "https://get.helm.sh/helm-${helm_version}-linux-amd64.tar.gz" --output "${helm_archive}"

tar xfz "${helm_archive}" -C "${tmpdir}" &> /dev/null
mv "${tmpdir}/linux-amd64/helm" "${bindir}"
