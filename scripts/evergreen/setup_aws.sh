#!/usr/bin/env bash
set -Eeou pipefail

source scripts/dev/set_env_context.sh
source scripts/funcs/install
source scripts/funcs/binary_cache

# AWS CLI version for caching (we use "v2-latest" as the key since AWS CLI doesn't pin versions well)
# The actual version downloaded is the latest v2
AWS_CLI_CACHE_VERSION="v2-latest"

# Initialize cache (if available) - done early for use in functions
CACHE_AVAILABLE=false
if init_cache_dir; then
    CACHE_AVAILABLE=true
fi

# Install AWS CLI v2 via binary download (for amd64 and arm64)
install_aws_cli_binary() {
    local arch="$1"
    echo "Installing AWS CLI v2 via binary download for ${arch}..."

    # Map architecture names for AWS CLI download URLs
    local aws_arch
    aws_arch=$(uname -m)

    # Check cache for the zip file first
    local temp_dir
    temp_dir=$(mktemp -d)
    local zip_file="${temp_dir}/awscliv2.zip"

    if [[ "$CACHE_AVAILABLE" == "true" ]] && get_cached_archive "awscli" "${AWS_CLI_CACHE_VERSION}-${aws_arch}" "${zip_file}"; then
        echo "AWS CLI archive restored from cache"
    else
        echo "Downloading AWS CLI v2 for ${aws_arch}..."
        curl_with_retry -s "https://awscli.amazonaws.com/awscli-exe-linux-${aws_arch}.zip" -o "${zip_file}"
        if [[ "$CACHE_AVAILABLE" == "true" ]]; then
            cache_archive "awscli" "${AWS_CLI_CACHE_VERSION}-${aws_arch}" "${zip_file}"
        fi
    fi

    cd "${temp_dir}"
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
