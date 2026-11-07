#!/usr/bin/env bash
set -Eeou pipefail

export VERSION=${VERSION:-1.16.1}
ISTIO_SCRIPT_CHECKSUM="254c6bd6aa5b8ac8c552561c84d8e9b3a101d9e613e2a8edd6db1f19c1871dbf"
# The vendored script checks the release with a single HEAD request and reports every non-2xx answer as
# "is not found". In the 2026-11-05 run three tasks lost 11s each to that message while other tasks in the same
# run downloaded the same archive, so a short server-side 5xx window is the observed failure mode. Three
# attempts with a 10s pause cover a window of that length without lengthening the setup step by more than a
# minute.
DOWNLOAD_ATTEMPTS=3
DOWNLOAD_RETRY_DELAY_SECONDS=10

download_istio() {
    if [ -d "istio-${VERSION}" ]; then
        echo "Istio ${VERSION} already downloaded... Skipping."
        return 0
    fi

    echo "Downloading Istio ${VERSION}"
    local attempt
    for ((attempt = 1; attempt <= DOWNLOAD_ATTEMPTS; attempt++)); do
        if ! curl --retry 5 --retry-delay 5 --retry-all-errors --fail --show-error --max-time 120 -O \
            https://raw.githubusercontent.com/istio/istio/d710dfc2f95adb9399e1656165fa5ac22f6e1a16/release/downloadIstioCandidate.sh; then
            echo "download_istio: attempt ${attempt}/${DOWNLOAD_ATTEMPTS} could not fetch the release script" >&2
        elif ! echo "${ISTIO_SCRIPT_CHECKSUM}  downloadIstioCandidate.sh" | sha256sum --check; then
            # A checksum mismatch is a permanent configuration error, not a transient one.
            echo "download_istio: downloadIstioCandidate.sh does not match the pinned checksum, refusing to run it" >&2
            return 1
        elif ISTIO_VERSION=${VERSION} sh downloadIstioCandidate.sh; then
            return 0
        else
            echo "download_istio: attempt ${attempt}/${DOWNLOAD_ATTEMPTS} could not download the Istio ${VERSION} archive" >&2
            # The archive extraction creates the directory. Remove a partial tree so the guard above cannot
            # make a later attempt or a later task skip the download.
            rm -rf "istio-${VERSION}"
        fi
        sleep "${DOWNLOAD_RETRY_DELAY_SECONDS}"
    done

    echo "download_istio: failed to download Istio ${VERSION} after ${DOWNLOAD_ATTEMPTS} attempts from https://github.com/istio/istio/releases/tag/${VERSION}" >&2
    return 1
}

download_istio

