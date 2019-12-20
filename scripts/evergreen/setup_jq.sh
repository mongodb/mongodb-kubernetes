#!/usr/bin/env bash
#
# A script Evergreen will use to setup jq
#
# This should be executed from root of the evergreen build dir
#

set -o nounset
set -xeo pipefail

BINDIR="${workdir}/bin"
mkdir -p "${BINDIR}" || true

echo "Downloading jq"
curl --retry 3 --silent -L https://github.com/stedolan/jq/releases/download/jq-1.6/jq-linux64 -o jq
chmod +x jq
mv jq "${BINDIR}"
echo "Installed jq to ${BINDIR}"
