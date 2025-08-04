#!/usr/bin/env bash
#
# A script Evergreen will use to setup jq
#
# This should be executed from root of the evergreen build dir
#

set -Eeou pipefail

source scripts/funcs/install

# Detect and map architecture for jq releases
detect_jq_architecture() {
    local arch
    arch=$(uname -m)

    case "${arch}" in
        x86_64)
            echo "amd64"
            ;;
        aarch64|arm64)
            echo "arm64"
            ;;
        ppc64le)
            echo "ppc64el"  # jq uses ppc64el instead of ppc64le
            ;;
        s390x)
            echo "s390x"
            ;;
        *)
            echo "Error: Unsupported architecture for jq: ${arch}" >&2
            exit 1
            ;;
    esac
}

jq_arch=$(detect_jq_architecture)
echo "Detected architecture: $(uname -m), using jq architecture: ${jq_arch}"

download_and_install_binary "${PROJECT_DIR:-${workdir:-.}}/bin" jq "https://github.com/stedolan/jq/releases/download/jq-1.8.1/jq-linux-${jq_arch}"
