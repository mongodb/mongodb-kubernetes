#!/usr/bin/env bash
#
# A script Evergreen will use to setup jq
#
# This should be executed from root of the evergreen build dir
#

set -Eeou pipefail

source scripts/funcs/install

jq_arch=$(detect_architecture "jq")
echo "Detected architecture: ${jq_arch}"

download_and_install_binary "${PROJECT_DIR:-${workdir}}/bin" jq "https://github.com/stedolan/jq/releases/download/jq-1.8.1/jq-linux-${jq_arch}"
