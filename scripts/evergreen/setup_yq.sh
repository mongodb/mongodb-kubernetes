#!/usr/bin/env bash

# A script Evergreen will use to setup yq
#
# This should be executed from root of the evergreen build dir

set -Eeoux pipefail

source scripts/funcs/install

download_and_install_binary "${workdir:-.}/bin" yq "https://github.com/mikefarah/yq/releases/download/v4.31.1/yq_linux_amd64"
