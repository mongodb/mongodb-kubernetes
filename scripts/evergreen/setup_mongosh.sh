#!/usr/bin/env bash
set -Eeou pipefail

source scripts/dev/set_env_context.sh

bindir="${workdir:?}/bin"
tmpdir="${workdir:?}/tmp"
mkdir -p "${bindir}" "${tmpdir}"

# Download mongosh archive
curl --retry 5 --retry-delay 3 --retry-all-errors --fail --show-error --max-time 180 --silent -LO "https://downloads.mongodb.com/compass/mongosh-2.3.8-linux-x64.tgz"
tar -zxvf mongosh-2.3.8-linux-x64.tgz -C "${tmpdir}"
cd "${tmpdir}/mongosh-2.3.8-linux-x64/bin"
./mongosh --version
mv mongosh "${bindir}"
