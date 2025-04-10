#!/usr/bin/env bash
set -Eeou pipefail

source scripts/dev/set_env_context.sh

INSTALL_DIR="${workdir:?}/.local/lib/aws"
BIN_LOCATION="${workdir}/bin"

mkdir -p "${BIN_LOCATION}"

tmpdir=$(mktemp -d)
cd "${tmpdir}"

curl "https://awscli.amazonaws.com/awscli-exe-linux-x86_64.zip" -o "awscliv2.zip"
unzip awscliv2.zip &> /dev/null

docker_dir="/home/${USER}/.docker"
if [[ ! -d "${docker_dir}" ]]; then
  mkdir -p "${docker_dir}"
fi

sudo chown "${USER}":"${USER}" "${docker_dir}" -R
sudo chmod g+rwx "${docker_dir}" -R
sudo ./aws/install --bin-dir "${BIN_LOCATION}" --install-dir "${INSTALL_DIR}" --update
cd -

rm -rf "${tmpdir}"
