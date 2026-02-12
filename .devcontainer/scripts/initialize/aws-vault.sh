#!/bin/bash
# init-aws-vault.sh - Runs on the HOST before container creation.
#
# Translates aws-vault server-mode credential environment variables so the
# AWS SDK inside the container can reach the credential server on the host.
#
# Prerequisites:
#   1. Start aws-vault in server mode on the host:
#        aws-vault exec <profile> --server
#   2. Open or rebuild the devcontainer — this script runs automatically.
#
# If aws-vault is not running, an empty .env file is created so that the
# Docker --env-file flag does not fail.

set -euo pipefail

ENV_FILE="${ENV_DIR}/aws-vault.env"

# Always create the file (truncate if exists) so --env-file never fails
true > "${ENV_FILE}"

if [ -n "${AWS_CONTAINER_CREDENTIALS_FULL_URI:-}" ]; then
    # Rewrite 127.0.0.1 / localhost to host.docker.internal so the container
    # can reach the aws-vault credential server running on the host.
    URI=$(echo "${AWS_CONTAINER_CREDENTIALS_FULL_URI}" \
        | sed 's/127\.0\.0\.1/host.docker.internal/g; s/localhost/host.docker.internal/g')
    echo "AWS_CONTAINER_CREDENTIALS_FULL_URI=${URI}" >> "${ENV_FILE}"
fi

if [ -n "${AWS_CONTAINER_CREDENTIALS_AUTHORIZATION_TOKEN:-}" ]; then
    echo "AWS_CONTAINER_CREDENTIALS_AUTHORIZATION_TOKEN=${AWS_CONTAINER_CREDENTIALS_AUTHORIZATION_TOKEN}" >> "${ENV_FILE}"
fi
