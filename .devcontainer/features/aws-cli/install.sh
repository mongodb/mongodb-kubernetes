#!/usr/bin/env bash
# Devcontainer feature: aws-cli (quiet)
#
# Local drop-in for ghcr.io/devcontainers/features/aws-cli:1. The upstream
# feature runs a bare `unzip awscliv2.zip`, which prints one `inflating:` line
# per file in the CLI bundle (hundreds of `examples/**/*.rst`) and floods the
# container build log. This installs the same AWS CLI v2 with `unzip -q` so the
# build log stays readable. Errors still surface on stderr.

set -euo pipefail

VERSION="${VERSION:-latest}"

case "$(uname -m)" in
    x86_64 | amd64) ARCH="x86_64" ;;
    aarch64 | arm64) ARCH="aarch64" ;;
    *)
        echo "aws-cli feature: unsupported architecture $(uname -m)" >&2
        exit 1
        ;;
esac

if [ "${VERSION}" = "latest" ]; then
    URL="https://awscli.amazonaws.com/awscli-exe-linux-${ARCH}.zip"
else
    URL="https://awscli.amazonaws.com/awscli-exe-linux-${ARCH}-${VERSION}.zip"
fi

# unzip is required and not guaranteed on the base image.
if ! command -v unzip >/dev/null 2>&1; then
    apt-get update
    apt-get install -y unzip
fi

WORK="$(mktemp -d)"
trap 'rm -rf "${WORK}"' EXIT

echo "aws-cli feature: installing AWS CLI v2 (${VERSION}, ${ARCH})..."
curl -fsSL "${URL}" -o "${WORK}/awscliv2.zip"
unzip -q "${WORK}/awscliv2.zip" -d "${WORK}"
"${WORK}/aws/install" --update >/dev/null

echo "aws-cli feature: $(aws --version)"
