#!/usr/bin/env bash
set -Eeou pipefail

source scripts/dev/set_env_context.sh

# Detect system architecture and map to AWS CLI architecture names
detect_aws_architecture() {
    local arch
    arch=$(uname -m)

    case "${arch}" in
        x86_64)
            echo "x86_64"
            ;;
        aarch64|arm64)
            echo "aarch64"
            ;;
        ppc64le)
            echo "Skipping AWS CLI installation: ppc64le (IBM Power) architecture is not supported by AWS CLI v2." >&2
            echo "AWS CLI v2 only supports: x86_64 (amd64), aarch64 (arm64)" >&2
            exit 0
            ;;
        s390x)
            echo "Skipping AWS CLI installation: s390x (IBM Z) architecture is not supported by AWS CLI v2." >&2
            echo "AWS CLI v2 only supports: x86_64 (amd64), aarch64 (arm64)" >&2
            exit 0
            ;;
        *)
            echo "Skipping AWS CLI installation: Unsupported architecture: ${arch}" >&2
            echo "AWS CLI v2 only supports: x86_64 (amd64), aarch64 (arm64)" >&2
            exit 0
            ;;
    esac
}

# Detect the current architecture
ARCH=$(detect_aws_architecture)
echo "Detected architecture: ${ARCH} (AWS CLI v2 supported)"

INSTALL_DIR="${workdir:?}/.local/lib/aws"
BIN_LOCATION="${workdir}/bin"

mkdir -p "${BIN_LOCATION}"

tmpdir=$(mktemp -d)
cd "${tmpdir}"

echo "Downloading AWS CLI v2 for ${ARCH}..."
curl "https://awscli.amazonaws.com/awscli-exe-linux-${ARCH}.zip" -o "awscliv2.zip"
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
