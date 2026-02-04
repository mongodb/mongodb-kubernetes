#!/usr/bin/env bash
#
# A script Evergreen will use to setup jq
#
# This should be executed from root of the evergreen build dir
#

set -Eeou pipefail

source scripts/funcs/install
source scripts/funcs/binary_cache

jq_arch=$(detect_architecture "jq")
echo "Detected architecture: ${jq_arch}"

jq_version="1.8.1"
bindir="${PROJECT_DIR:-${workdir}}/bin"
mkdir -p "${bindir}"

# Initialize cache (if available)
cache_available=false
if init_cache_dir; then
    cache_available=true
fi

if [[ "$cache_available" == "true" ]] && get_cached_binary "jq" "${jq_version}" "${bindir}/jq"; then
    echo "jq restored from cache"
else
    # Use jqlang/jq (canonical repo) directly to avoid redirect from stedolan/jq
    curl_with_retry -L "https://github.com/jqlang/jq/releases/download/jq-${jq_version}/jq-linux-${jq_arch}" -o jq
    chmod +x jq
    mv jq "${bindir}"
    echo "Installed jq to ${bindir}"
    if [[ "$cache_available" == "true" ]]; then
        cache_binary "jq" "${jq_version}" "${bindir}/jq"
    fi
fi
