#!/usr/bin/env bash
set -Eeou pipefail

bindir="${workdir:?}/bin"
mkdir -p "${bindir}"

echo "Downloading kubectl v1.15.11"
curl -s -LO https://storage.googleapis.com/kubernetes-release/release/v1.15.11/bin/linux/amd64/kubectl
chmod +x kubectl
echo "kubectl version --client"
./kubectl version --client
mv kubectl "${bindir}"

echo "Downloading helm"
helm=helm.tgz
helm_version="v3.2.1"
curl -s https://get.helm.sh/helm-${helm_version}-linux-amd64.tar.gz --output "${helm}"
tar xfz "${helm}" &> /dev/null
mv linux-amd64/helm "${bindir}"
rm "${helm}"
