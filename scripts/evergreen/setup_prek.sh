#!/usr/bin/env bash
#
# Downloads and installs prek (Rust reimplementation of pre-commit)
# from GitHub releases into ${PROJECT_DIR}/bin.
#

set -Eeou pipefail

source scripts/dev/set_env_context.sh
source scripts/funcs/install

PREK_VERSION="v0.4.6"
bindir="${PROJECT_DIR}/bin"
tmpdir="${PROJECT_DIR}/tmp"
mkdir -p "${bindir}" "${tmpdir}"

os="$(uname -s)"
arch="$(uname -m)"

case "${os}-${arch}" in
  Linux-x86_64)   triple="x86_64-unknown-linux-gnu" ;;
  Linux-aarch64)  triple="aarch64-unknown-linux-gnu" ;;
  Linux-s390x)    triple="s390x-unknown-linux-gnu" ;;
  Darwin-arm64)   triple="aarch64-apple-darwin" ;;
  Darwin-x86_64)  triple="x86_64-apple-darwin" ;;
  *) echo "Error: unsupported platform ${os}-${arch}" >&2; exit 1 ;;
esac

base_url="https://github.com/j178/prek/releases/download/${PREK_VERSION}"
tarball="prek-${triple}.tar.gz"

pushd "${tmpdir}" >/dev/null

echo "Downloading prek ${PREK_VERSION} for ${triple}..."
curl_with_retry -L "${base_url}/${tarball}" -o "${tarball}"
curl_with_retry -L "${base_url}/${tarball}.sha256" -o "${tarball}.sha256"

echo "Verifying checksum..."
# ponytail: sha256sum on Linux, shasum -a 256 on macOS
if command -v sha256sum >/dev/null 2>&1; then
  sha256sum -c "${tarball}.sha256" --status
else
  shasum -a 256 -c "${tarball}.sha256" --status
fi

tar --strip-components=1 -xf "${tarball}"
chmod +x prek
mv prek "${bindir}/"
popd >/dev/null

"${bindir}/prek" --version
echo "prek installed to ${bindir}/prek"
