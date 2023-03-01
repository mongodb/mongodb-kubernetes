#!/usr/bin/env bash

# Script for evergreen to setup necessary software for generating openshift bundles.
#
# This should be executed from root of the evergreen build dir

set -Eeoux pipefail

source scripts/funcs/install

download_and_install_binary "${workdir:-.}/bin" operator-sdk "https://github.com/operator-framework/operator-sdk/releases/download/v1.26.1/operator-sdk_linux_amd64"
download_and_install_binary "${workdir:-.}/bin" operator-manifest-tools "https://github.com/operator-framework/operator-manifest-tools/releases/download/v0.2.2/operator-manifest-tools_0.2.2_linux_amd64"
download_and_install_binary "${workdir:-.}/bin" crane "https://github.com/michaelsauter/crane/releases/download/v3.6.1/crane_linux_amd64"
