#!/usr/bin/env bash
set -Eeou pipefail

source scripts/dev/set_env_context.sh
source scripts/funcs/install

# The AWS CLI v2 installer ships a directory tree plus a `aws` entry point, so it
# installs into ${PROJECT_DIR} rather than /usr/local. That keeps the same
# ${PROJECT_DIR}/bin guard as every other tool, and it needs no sudo.
fetch_aws_cli() {
    local aws_arch
    aws_arch=$(uname -m)

    local temp_dir
    temp_dir=$(mktemp -d)

    echo "Downloading AWS CLI v2 for ${aws_arch}..."
    curl_with_retry -s "https://awscli.amazonaws.com/awscli-exe-linux-${aws_arch}.zip" -o "${temp_dir}/awscliv2.zip"

    pushd "${temp_dir}" > /dev/null
    unzip -q awscliv2.zip
    mkdir -p "${PROJECT_DIR}/bin"
    ./aws/install --update --install-dir "${PROJECT_DIR}/aws-cli" --bin-dir "${PROJECT_DIR}/bin"
    popd > /dev/null

    rm -rf "${temp_dir}"
}

# Install AWS CLI v1 via pip (for IBM architectures: ppc64le, s390x)
install_aws_cli_pip() {
    echo "Installing AWS CLI v1 via pip (for IBM architectures)..."

    # The venv is activated by set_env_context.sh (sourced at the top of this
    # script); fail clearly if it is not on PATH.
    if [[ -z "${VIRTUAL_ENV:-}" ]]; then
        echo "Error: Python venv not active. Please run recreate_python_venv.sh first." >&2
        return 1
    fi

    # Check if AWS CLI exists and works in the venv
    if command -v aws &> /dev/null; then
        if aws --version > /dev/null 2>&1; then
            echo "AWS CLI is already installed and working in venv"
            return 0
        else
            echo "AWS CLI exists but not functional, reinstalling in venv..."
            pip uninstall -y awscli || true
        fi
    fi

    echo "Installing AWS CLI into venv using pip..."
    pip install awscli

    # Verify installation
    if command -v aws &> /dev/null; then
        echo "AWS CLI v1 installed successfully in venv"
    else
        echo "Error: AWS CLI v1 installation failed" >&2
        return 1
    fi
}

# Main installation logic
install_aws_cli() {
    local arch
    arch=$(detect_architecture)

    case "${arch}" in
        amd64|arm64)
            echo "Standard architecture detected (${arch}). Using binary installation..."
            ensure_binary aws fetch_aws_cli
            ;;
        *)
            echo "Warning: Unknown architecture ${arch}. Falling back to pip installation..."
            install_aws_cli_pip
            ;;
    esac
}

install_aws_cli

docker_dir="/home/${USER}/.docker"
if [[ ! -d "${docker_dir}" ]]; then
  mkdir -p "${docker_dir}"
fi

sudo chown "${USER}":"${USER}" "${docker_dir}" -R
sudo chmod g+rwx "${docker_dir}" -R

echo "AWS CLI setup completed successfully."
