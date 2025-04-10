#!/usr/bin/env bash
set -Eeou pipefail

source scripts/dev/set_env_context.sh

bindir="${workdir:?}/bin"
mkdir -p "${bindir}"

curl -s --retry 3 -LO "https://downloads.mongodb.com/compass/mongosh-2.3.8-linux-x64.tgz"
tar -zxvf mongosh-2.3.8-linux-x64.tgz
cd mongosh-2.3.8-linux-x64/bin
./mongosh --version
mv mongosh "${bindir}"
