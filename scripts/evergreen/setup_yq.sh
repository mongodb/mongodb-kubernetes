#!/usr/bin/env bash

# A script Evergreen will use to setup yq
#
# This should be executed from root of the evergreen build dir

set -Eeou pipefail -o posix

source scripts/funcs/install
source scripts/dev/set_env_context.sh

download_and_install_binary "${PROJECT_DIR:-.}/bin" yq "https://github.com/mikefarah/yq/releases/download/v4.31.1/yq_linux_amd64"
