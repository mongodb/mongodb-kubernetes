#!/usr/bin/env bash

# A script Evergreen will use to setup yq
#
# This should be executed from root of the evergreen build dir

set -Eeou pipefail

source scripts/funcs/install
source scripts/dev/set_env_context.sh
source scripts/funcs/binary_cache

yq_version="v4.31.1"
bindir="${PROJECT_DIR:-.}/bin"
mkdir -p "${bindir}"

# Initialize cache (if available)
cache_available=false
if init_cache_dir; then
    cache_available=true
fi

if [[ "$cache_available" == "true" ]] && get_cached_binary "yq" "${yq_version}" "${bindir}/yq"; then
    echo "yq restored from cache"
else
    curl_with_retry -L "https://github.com/mikefarah/yq/releases/download/${yq_version}/yq_linux_amd64" -o yq
    chmod +x yq
    mv yq "${bindir}"
    echo "Installed yq to ${bindir}"
    if [[ "$cache_available" == "true" ]]; then
        cache_binary "yq" "${yq_version}" "${bindir}/yq"
    fi
fi
