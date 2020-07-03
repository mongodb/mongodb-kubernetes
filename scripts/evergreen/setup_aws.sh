#!/usr/bin/env bash
set -Eeou pipefail

#
# This script should be run from the root evergreen work dir

INSTALL_DIR="${workdir:?}/.local/lib/aws"
BIN_LOCATION="${workdir}/bin/aws"

curl -s "https://s3.amazonaws.com/aws-cli/awscli-bundle.zip" -o "awscli-bundle.zip"
unzip awscli-bundle.zip &> /dev/null
./awscli-bundle/install --bin-location "${BIN_LOCATION}" --install-dir "${INSTALL_DIR}"
