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

# This should be changed when needed
jq_version="1.8.1"

fetch_jq() {
  # Use jqlang/jq (canonical repo) directly to avoid redirect from stedolan/jq
  download_and_install_binary "${PROJECT_DIR:-${workdir:?}}/bin" jq \
    "https://github.com/jqlang/jq/releases/download/jq-${jq_version}/jq-linux-${jq_arch}"
}

ensure_binary jq fetch_jq
