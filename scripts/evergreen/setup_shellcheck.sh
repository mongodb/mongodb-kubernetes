#!/usr/bin/env bash
set -Eeou pipefail

source scripts/dev/set_env_context.sh

bindir="${PROJECT_DIR:?}/bin"
mkdir -p "${PROJECT_DIR}/bin/"

echo "Downloading shellcheck"
shellcheck=shellcheck.tar.xz
shellcheck_version="v0.9.0"
curl --retry 3 --silent -L "https://github.com/koalaman/shellcheck/releases/download/${shellcheck_version}/shellcheck-${shellcheck_version}.linux.x86_64.tar.xz" -o ${shellcheck}
tar xf "${shellcheck}"
mv shellcheck-"${shellcheck_version}"/shellcheck "${bindir}"
rm "${shellcheck}"
