#!/usr/bin/env bash
#
# A script Evergreen will use to setup jq
#
# This should be executed from root of the evergreen build dir
#

set -o nounset
set -xeo pipefail

echo "Downloading jq"
curl -L https://github.com/stedolan/jq/releases/download/jq-1.6/jq-linux64 -o jq
chmod +x jq
mkdir -p ${WORKDIR}/bin/
mv jq ${WORKDIR}/bin/
echo "Installed jq to ${WORKDIR}/bin/"
