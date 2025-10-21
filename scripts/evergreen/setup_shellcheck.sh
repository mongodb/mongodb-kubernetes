#!/usr/bin/env bash
set -Eeou pipefail

source scripts/dev/set_env_context.sh

bindir="${PROJECT_DIR:?}/bin"
tmpdir="${PROJECT_DIR:?}/tmp"
mkdir -p "${bindir}" "${tmpdir}"

echo "Downloading shellcheck"
shellcheck_archive="${tmpdir}/shellcheck.tar.xz"
shellcheck_version="v0.9.0"
curl --retry 5 --fail --show-error --max-time 180 --silent -L "https://github.com/koalaman/shellcheck/releases/download/${shellcheck_version}/shellcheck-${shellcheck_version}.linux.x86_64.tar.xz" -o "${shellcheck_archive}"
tar -xf "${shellcheck_archive}" -C "${tmpdir}"
mv "${tmpdir}/shellcheck-${shellcheck_version}/shellcheck" "${bindir}"
rm "${shellcheck_archive}"
