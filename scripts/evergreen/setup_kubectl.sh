#!/usr/bin/env bash
set -Eeou pipefail

source scripts/dev/set_env_context.sh

bindir="${PROJECT_DIR}/bin"
mkdir -p "${bindir}"

echo "Downloading latest kubectl"
curl -s --retry 3 -LO "https://dl.k8s.io/release/$(curl -L -s https://dl.k8s.io/release/stable.txt)/bin/linux/amd64/kubectl"
chmod +x kubectl
echo "kubectl version --client"
./kubectl version --client
mv kubectl "${bindir}"

echo "Downloading helm"
helm=helm.tgz
helm_version="v3.13.0"
curl -s https://get.helm.sh/helm-${helm_version}-linux-amd64.tar.gz --output "${helm}"
tar xfz "${helm}" &> /dev/null
mv linux-amd64/helm "${bindir}"
rm "${helm}"
