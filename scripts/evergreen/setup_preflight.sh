#!/usr/bin/env bash
#
# A script Evergreen will use to setup openshift-preflight
set -Eeou pipefail
source scripts/dev/set_env_context.sh
source scripts/funcs/install
source scripts/funcs/binary_cache

preflight_version="1.14.1"
bindir="${PROJECT_DIR:?}/bin"
mkdir -p "${bindir}"

# Initialize cache (if available)
cache_available=false
if init_cache_dir; then
    cache_available=true
fi

if [[ "$cache_available" == "true" ]] && get_cached_binary "preflight" "${preflight_version}" "${bindir}/preflight"; then
    echo "preflight restored from cache"
else
    echo "Downloading preflight binary"
    curl_with_retry -s -o preflight -L "https://github.com/redhat-openshift-ecosystem/openshift-preflight/releases/download/${preflight_version}/preflight-linux-amd64"
    chmod +x preflight
    mv preflight "${bindir}"
    echo "Installed preflight to ${bindir}"
    if [[ "$cache_available" == "true" ]]; then
        cache_binary "preflight" "${preflight_version}" "${bindir}/preflight"
    fi
fi
