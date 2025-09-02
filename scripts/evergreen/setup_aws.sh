#!/usr/bin/env bash
set -Eeou pipefail

source scripts/dev/set_env_context.sh
source scripts/funcs/install

# Install AWS CLI v2 via binary download (for amd64 and arm64)
install_aws_cli_binary() {
    local arch="$1"
    echo "Installing AWS CLI v2 via binary download for ${arch}..."

    # Map architecture names for AWS CLI download URLs
    local aws_arch
    aws_arch=$(uname -m)

    # Download and install AWS CLI v2
    local temp_dir
    temp_dir=$(mktemp -d)
    cd "${temp_dir}"

    echo "Downloading AWS CLI v2 for ${aws_arch}..."
    curl -s "https://awscli.amazonaws.com/awscli-exe-linux-${aws_arch}.zip" -o "awscliv2.zip"

    unzip -q awscliv2.zip
    sudo ./aws/install --update

    # Clean up
    cd - > /dev/null
    rm -rf "${temp_dir}"

    # Verify installation
    if command -v aws &> /dev/null; then
        echo "AWS CLI v2 installed successfully:"
        aws --version
    else
        echo "Error: AWS CLI v2 installation failed" >&2
        return 1
    fi
}

# Install AWS CLI v1 via pip (for IBM architectures: ppc64le, s390x)
install_aws_cli_pip() {
    echo "Installing AWS CLI v1 via pip (for IBM architectures)..."

    if [[ ! -d "${PROJECT_DIR}/venv" ]]; then
        echo "Error: Python venv not found at ${PROJECT_DIR}/venv. Please run recreate_python_venv.sh first." >&2
        return 1
    fi

    # Activate the venv
    source "${PROJECT_DIR}/venv/bin/activate"

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
            install_aws_cli_binary "${arch}"
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
