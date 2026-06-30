#!/usr/bin/env bash
set -Eeou pipefail

source scripts/funcs/install

GH_VERSION="2.50.0"
arch=$(detect_architecture)
tarball="gh_${GH_VERSION}_linux_${arch}.tar.gz"
url="https://github.com/cli/cli/releases/download/v${GH_VERSION}/${tarball}"
bin_dir="${PROJECT_DIR:-${workdir}}/bin"

mkdir -p "${bin_dir}"
echo "Downloading gh ${GH_VERSION} (${arch})..."
curl_with_retry -L "${url}" -o "${tarball}"
tar -xzf "${tarball}" "gh_${GH_VERSION}_linux_${arch}/bin/gh"
mv "gh_${GH_VERSION}_linux_${arch}/bin/gh" "${bin_dir}/gh"
chmod +x "${bin_dir}/gh"
rm -rf "${tarball}" "gh_${GH_VERSION}_linux_${arch}"
echo "Installed gh to ${bin_dir}/gh"
