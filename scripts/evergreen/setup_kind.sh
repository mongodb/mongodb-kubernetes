#!/usr/bin/env bash
set -Eeou pipefail

source scripts/dev/set_env_context.sh
source scripts/funcs/install
source scripts/funcs/binary_cache

# Store the lowercase name of Operating System
os=$(uname | tr '[:upper:]' '[:lower:]')
# Detect architecture
arch_suffix=$(detect_architecture)
# This should be changed when needed
latest_version="v0.29.0"

# Only proceed with installation if architecture is supported (amd64 or arm64)
if [[ "${arch_suffix}" == "amd64" || "${arch_suffix}" == "arm64" ]]; then
    bindir="${PROJECT_DIR}/bin"
    mkdir -p "${bindir}"

    # Initialize cache (if available)
    cache_available=false
    if init_cache_dir; then
        cache_available=true
    fi

    if [[ "$cache_available" == "true" ]] && get_cached_binary "kind" "${latest_version}" "${bindir}/kind"; then
        echo "kind restored from cache"
    else
        echo "Saving kind to ${bindir}"
        curl_with_retry -L "https://github.com/kubernetes-sigs/kind/releases/download/${latest_version}/kind-${os}-${arch_suffix}" -o kind
        chmod +x kind
        sudo mv kind "${bindir}"
        if [[ "$cache_available" == "true" ]]; then
            cache_binary "kind" "${latest_version}" "${bindir}/kind"
        fi
    fi
    echo "Installed kind in ${bindir}"
else
    echo "Architecture ${arch_suffix} not supported for kind installation, skipping"
fi
