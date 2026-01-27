#!/usr/bin/env bash
set -Eeou pipefail

source scripts/dev/set_env_context.sh

bindir="${workdir:?}/bin"
mkdir -p "${bindir}"

curl --retry 10 --retry-delay 10 --retry-all-errors --max-time 300 -fsSLO "https://downloads.mongodb.com/compass/mongosh-2.3.8-linux-x64.tgz"
tar -zxvf mongosh-2.3.8-linux-x64.tgz
cd mongosh-2.3.8-linux-x64/bin
./mongosh --version
mv mongosh "${bindir}"
