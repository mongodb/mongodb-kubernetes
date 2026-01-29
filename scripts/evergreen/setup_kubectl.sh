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
echo "Downloading kubectl ${KUBECTL_VERSION} for ${ARCH}"

# kubectl needs special handling because:
# 1. dl.k8s.io has experienced 503 outages that outlast our retry window
# 2. Unlike other tools, kubectl binaries aren't available on GitHub releases
# 3. dl.k8s.io redirects to cdn.dl.k8s.io (Fastly), so we try the CDN directly as fallback
kubectl_url="https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/${ARCH}/kubectl"
kubectl_cdn_url="https://cdn.dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/${ARCH}/kubectl"

if ! curl_with_retry -LOs "${kubectl_url}"; then
    echo "Primary endpoint failed, trying CDN directly..."
    curl_with_retry -LOs "${kubectl_cdn_url}"
fi

chmod +x kubectl
echo "kubectl version --client"
./kubectl version --client
mv kubectl "${bindir}"

echo "Downloading helm ${HELM_VERSION} for ${ARCH}"
helm_archive="${tmpdir}/helm.tgz"
curl_with_retry -s "https://get.helm.sh/helm-${HELM_VERSION}-linux-${ARCH}.tar.gz" --output "${helm_archive}"

tar xfz "${helm_archive}" -C "${tmpdir}" &> /dev/null
mv "${tmpdir}/linux-${ARCH}/helm" "${bindir}"
"${bindir}"/helm version
