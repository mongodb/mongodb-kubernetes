#!/usr/bin/env bash
#
# Installs the trivy vulnerability scanner (https://github.com/aquasecurity/trivy)
# into ${PROJECT_DIR}/bin. Used by the CVE image scanning step of the
# build-and-publish pipeline (scripts/release/trivy_scan.sh).
set -Eeou pipefail

source scripts/dev/set_env_context.sh
source scripts/funcs/install

trivy_version="${TRIVY_VERSION:-0.74.0}"
bindir="${PROJECT_DIR:?}/bin"
mkdir -p "${bindir}"

if [[ -x "${bindir}/trivy" ]] && "${bindir}/trivy" --version 2>/dev/null | grep -q "Version: ${trivy_version}"; then
    echo "trivy ${trivy_version} already installed in ${bindir}. Skipping the installation."
    exit 0
fi

# Map the host architecture to trivy's release artifact naming
arch="$(detect_architecture)"
case "${arch}" in
    amd64)
        trivy_arch="64bit"
        ;;
    arm64)
        trivy_arch="ARM64"
        ;;
    s390x)
        trivy_arch="s390x"
        ;;
    ppc64le)
        trivy_arch="PPC64LE"
        ;;
    *)
        echo "Error: unsupported architecture for trivy: ${arch}" >&2
        exit 1
        ;;
esac

tmpdir="$(mktemp -d)"
trap 'rm -rf "${tmpdir}"' EXIT

echo "Downloading trivy ${trivy_version} (Linux-${trivy_arch})"
curl_with_retry -sL -o "${tmpdir}/trivy.tar.gz" \
    "https://github.com/aquasecurity/trivy/releases/download/v${trivy_version}/trivy_${trivy_version}_Linux-${trivy_arch}.tar.gz"
tar -xzf "${tmpdir}/trivy.tar.gz" -C "${tmpdir}" trivy
chmod +x "${tmpdir}/trivy"
mv "${tmpdir}/trivy" "${bindir}/trivy"
echo "Installed trivy to ${bindir}"
