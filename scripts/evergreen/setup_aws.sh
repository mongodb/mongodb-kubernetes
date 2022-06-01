#!/usr/bin/env bash
set -Eeou pipefail
set -x
#
# This script should be run from the root evergreen work dir

INSTALL_DIR="${workdir:?}/.local/lib/aws"
BIN_LOCATION="${workdir}/bin"

mkdir -p "${BIN_LOCATION}"

curl "https://awscli.amazonaws.com/awscli-exe-linux-x86_64.zip" -o "awscliv2.zip"
unzip awscliv2.zip &> /dev/null
sudo chown "$USER":"$USER" /home/"$USER"/.docker -R
sudo chmod g+rwx "/home/$USER/.docker" -R
sudo ./aws/install --bin-dir "${BIN_LOCATION}" --install-dir "${INSTALL_DIR}" --update
