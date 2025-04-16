#!/usr/bin/env bash
#
# A script Evergreen will use to setup openshift-preflight
set -Eeou pipefail
source scripts/dev/set_env_context.sh

bindir="${PROJECT_DIR:?}/bin"
mkdir -p "${bindir}"

echo "Downloading preflight binary"
preflight_version="1.12.1"
curl -s --retry 3 --fail-with-body -o preflight -LO "https://github.com/redhat-openshift-ecosystem/openshift-preflight/releases/download/${preflight_version}/preflight-linux-amd64"
chmod +x preflight
mv preflight "${bindir}"
echo "Installed preflight to ${bindir}"
