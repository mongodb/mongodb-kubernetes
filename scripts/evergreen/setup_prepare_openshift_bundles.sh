#!/usr/bin/env bash

# Script for evergreen to setup necessary software for generating openshift bundles.
#
# This should be executed from root of the evergreen build dir

set -Eeou pipefail

source scripts/dev/set_env_context.sh

source scripts/funcs/install

OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m | tr '[:upper:]' '[:lower:]')
if [[ "${ARCH}" == "x86_64" ]]; then
  ARCH="amd64"
fi

download_and_install_binary "${PROJECT_DIR:-.}/bin" operator-sdk "https://github.com/operator-framework/operator-sdk/releases/download/v1.26.1/operator-sdk_${OS}_${ARCH}"
download_and_install_binary "${PROJECT_DIR:-.}/bin" operator-manifest-tools "https://github.com/operator-framework/operator-manifest-tools/releases/download/v0.2.2/operator-manifest-tools_0.2.2_${OS}_amd64"

if [[ "${OS}" == "darwin" ]]; then
  brew install skopeo
else
  sudo apt-get update
  sudo apt install -y skopeo
fi

opm_os="linux"
if [[ "${OS}" == "darwin" ]]; then
  opm_os="mac"
fi

# there is no mac build in for arm64
opm_arch="amd64"
curl --retry 5 --retry-delay 3 --retry-all-errors --fail --show-error --max-time 180 -L -o opm.tar.gz "https://mirror.openshift.com/pub/openshift-v4/${opm_arch}/clients/ocp/latest-4.12/opm-${opm_os}.tar.gz"

# TODO: Sometimes tar is failing for unknown reasons in EVG. This is left intentionally. Remove if not causing problems anymore.
ls -al opm.tar.gz
head -c 50 < opm.tar.gz | xxd

tar xvf opm.tar.gz
chmod +x opm && mv opm "${PROJECT_DIR:-.}/bin"
rm -rf opm.tar.gz
