#!/usr/bin/env bash

# Script for evergreen to setup necessary software for generating openshift bundles.
#
# This should be executed from root of the evergreen build dir

set -Eeou pipefail

source scripts/dev/set_env_context.sh
source scripts/funcs/install
source scripts/funcs/binary_cache

OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m | tr '[:upper:]' '[:lower:]')
if [[ "${ARCH}" == "x86_64" ]]; then
  ARCH="amd64"
fi

bindir="${PROJECT_DIR:-.}/bin"
mkdir -p "${bindir}"

# Initialize cache (if available)
cache_available=false
if init_cache_dir; then
    cache_available=true
fi

# Install operator-sdk with caching
operator_sdk_version="v1.26.1"
if [[ "$cache_available" == "true" ]] && get_cached_binary "operator-sdk" "${operator_sdk_version}" "${bindir}/operator-sdk"; then
    echo "operator-sdk restored from cache"
else
    curl_with_retry -L "https://github.com/operator-framework/operator-sdk/releases/download/${operator_sdk_version}/operator-sdk_${OS}_${ARCH}" -o operator-sdk
    chmod +x operator-sdk
    mv operator-sdk "${bindir}"
    echo "Installed operator-sdk to ${bindir}"
    if [[ "$cache_available" == "true" ]]; then
        cache_binary "operator-sdk" "${operator_sdk_version}" "${bindir}/operator-sdk"
    fi
fi

# Install operator-manifest-tools with caching
operator_manifest_tools_version="v0.2.2"
if [[ "$cache_available" == "true" ]] && get_cached_binary "operator-manifest-tools" "${operator_manifest_tools_version}" "${bindir}/operator-manifest-tools"; then
    echo "operator-manifest-tools restored from cache"
else
    curl_with_retry -L "https://github.com/operator-framework/operator-manifest-tools/releases/download/${operator_manifest_tools_version}/operator-manifest-tools_0.2.2_${OS}_amd64" -o operator-manifest-tools
    chmod +x operator-manifest-tools
    mv operator-manifest-tools "${bindir}"
    echo "Installed operator-manifest-tools to ${bindir}"
    if [[ "$cache_available" == "true" ]]; then
        cache_binary "operator-manifest-tools" "${operator_manifest_tools_version}" "${bindir}/operator-manifest-tools"
    fi
fi

# Install skopeo (system package, not cached)
if [[ "${OS}" == "darwin" ]]; then
  brew install skopeo
else
  sudo apt-get update
  sudo apt install -y skopeo
fi

# Install opm with caching
opm_os="linux"
if [[ "${OS}" == "darwin" ]]; then
  opm_os="mac"
fi

# there is no mac build in for arm64
opm_arch="amd64"
opm_version="latest-4.12"

if [[ "$cache_available" == "true" ]] && get_cached_binary "opm" "${opm_version}" "${bindir}/opm"; then
    echo "opm restored from cache"
else
    curl_with_retry -L -o opm.tar.gz "https://mirror.openshift.com/pub/openshift-v4/${opm_arch}/clients/ocp/${opm_version}/opm-${opm_os}.tar.gz"

    # TODO: Sometimes tar is failing for unknown reasons in EVG. This is left intentionally. Remove if not causing problems anymore.
    ls -al opm.tar.gz
    head -c 50 < opm.tar.gz | xxd

    tar xvf opm.tar.gz
    chmod +x opm && mv opm "${bindir}"
    rm -rf opm.tar.gz
    if [[ "$cache_available" == "true" ]]; then
        cache_binary "opm" "${opm_version}" "${bindir}/opm"
    fi
fi
