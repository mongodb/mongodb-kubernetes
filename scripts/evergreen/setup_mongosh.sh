#!/usr/bin/env bash
set -Eeou pipefail

source scripts/dev/set_env_context.sh
source scripts/funcs/install
source scripts/funcs/binary_cache

mongosh_version="2.3.8"
bindir="${workdir:?}/bin"
tmpdir="${workdir:?}/tmp"
mkdir -p "${bindir}" "${tmpdir}"

# Initialize cache (if available)
cache_available=false
if init_cache_dir; then
    cache_available=true
fi

if [[ "$cache_available" == "true" ]] && get_cached_binary "mongosh" "${mongosh_version}" "${bindir}/mongosh"; then
    echo "mongosh restored from cache"
    "${bindir}/mongosh" --version
else
    # Download mongosh archive
    curl_with_retry --silent -LO "https://downloads.mongodb.com/compass/mongosh-${mongosh_version}-linux-x64.tgz"
    tar -zxvf "mongosh-${mongosh_version}-linux-x64.tgz" -C "${tmpdir}"
    cd "${tmpdir}/mongosh-${mongosh_version}-linux-x64/bin"
    ./mongosh --version
    mv mongosh "${bindir}"
    rm -f "mongosh-${mongosh_version}-linux-x64.tgz"
    if [[ "$cache_available" == "true" ]]; then
        cache_binary "mongosh" "${mongosh_version}" "${bindir}/mongosh"
    fi
fi
