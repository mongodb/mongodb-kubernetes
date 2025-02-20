#!/usr/bin/env bash
set -Eeou pipefail

export VERSION=${VERSION:-1.16.1}
ISTIO_SCRIPT_CHECKSUM="254c6bd6aa5b8ac8c552561c84d8e9b3a101d9e613e2a8edd6db1f19c1871dbf"

echo "Checking if we need to download Istio ${VERSION}"
if [ ! -d "istio-${VERSION}" ]; then
    echo "Downloading Istio ${VERSION}"
    curl -O https://raw.githubusercontent.com/istio/istio/d710dfc2f95adb9399e1656165fa5ac22f6e1a16/release/downloadIstioCandidate.sh
    echo "${ISTIO_SCRIPT_CHECKSUM}  downloadIstioCandidate.sh" | sha256sum --check
    ISTIO_VERSION=${VERSION} sh downloadIstioCandidate.sh
else
    echo "Istio ${VERSION} already downloaded... Skipping."
fi
