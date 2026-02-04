#!/usr/bin/env bash
set -Eeou pipefail

source scripts/dev/set_env_context.sh
source scripts/funcs/install
source scripts/funcs/binary_cache

# Detect the current architecture
ARCH=$(detect_architecture)
echo "Detected architecture: ${ARCH}"

bindir="${PROJECT_DIR}/bin"
tmpdir="${PROJECT_DIR}/tmp"
mkdir -p "${bindir}" "${tmpdir}"

# Initialize cache (if available)
cache_available=false
if init_cache_dir; then
    cache_available=true
fi

# Install kubectl with caching
kubectl_version="${KUBECTL_VERSION}"
if [[ "$cache_available" == "true" ]] && get_cached_binary "kubectl" "${kubectl_version}" "${bindir}/kubectl"; then
    echo "kubectl restored from cache"
else
    download_kubectl_binary "${kubectl_version}" "${ARCH}"
    chmod +x kubectl
    mv kubectl "${bindir}"
    if [[ "$cache_available" == "true" ]]; then
        cache_binary "kubectl" "${kubectl_version}" "${bindir}/kubectl"
    fi
fi

echo "kubectl version --client"
"${bindir}"/kubectl version --client

# Install helm with caching
helm_version="${HELM_VERSION}"
if [[ "$cache_available" == "true" ]] && get_cached_binary "helm" "${helm_version}" "${bindir}/helm"; then
    echo "helm restored from cache"
else
    pushd "${tmpdir}" > /dev/null
    download_helm_binary "${helm_version}" "${ARCH}"
    mv "linux-${ARCH}/helm" "${bindir}"
    rm -rf "linux-${ARCH}/"
    popd > /dev/null
    if [[ "$cache_available" == "true" ]]; then
        cache_binary "helm" "${helm_version}" "${bindir}/helm"
    fi
fi

"${bindir}"/helm version
