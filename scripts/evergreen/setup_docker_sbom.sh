#!/usr/bin/env bash
set -Eeou pipefail

source scripts/dev/set_env_context.sh

if [ -f ~/.docker/cli-plugins/docker-sbom ]; then
    echo "Docker sbom exists. Skipping the installation."
else
    echo "Installing Docker sbom plugin."

    docker_dir="/home/${USER}/.docker"
    if [[ ! -d "${docker_dir}" ]]; then
      mkdir -p "${docker_dir}"
      sudo chown "${USER}":"${USER}" "${docker_dir}" -R
      sudo chmod g+rwx "${docker_dir}" -R
    fi

    plugins_dir="/home/${USER}/.docker/cli-plugins"
    mkdir -p "${plugins_dir}"
    sudo chown "${USER}":"${USER}" "${plugins_dir}" -R
    sudo chmod g+rwx "${plugins_dir}" -R
    wget "https://github.com/docker/sbom-cli-plugin/releases/download/v0.6.1/sbom-cli-plugin_0.6.1_linux_amd64.tar.gz"
    tar -zxf sbom-cli-plugin_0.6.1_linux_amd64.tar.gz
    chmod +x ./docker-sbom
    mv ./docker-sbom "${plugins_dir}"
    rm -rf sbom-cli-plugin_0.6.1_linux_amd64.tar.gz
fi

