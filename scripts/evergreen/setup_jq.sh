#!/usr/bin/env bash
#
# A script Evergreen will use to setup jq
#
# This should be executed from root of the evergreen build dir
#

set -Eeou pipefail

source scripts/dev/set_env_context.sh
source scripts/funcs/install

arch=$(uname -m)

download_and_install_binary "${PROJECT_DIR:-.}/bin" jq "https://github.com/stedolan/jq/releases/download/jq-1.8.1/jq-linux-${arch}"
