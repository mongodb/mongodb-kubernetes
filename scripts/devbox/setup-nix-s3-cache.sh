#!/usr/bin/env bash
# setup-nix-s3-cache.sh
#
# Configures AWS credentials for the Nix daemon and sets up the S3 binary
# cache as a Nix substituter. Designed for Evergreen CI hosts (Linux).
#
# Must run AFTER Nix is installed (e.g., via download_devbox) and BEFORE
# devbox install, so that packages are pulled from S3 instead of cache.nixos.org.
#
# The Nix daemon runs as root, so AWS credentials are written to /root/.aws/.
# configure-nix-cache.sh then writes /etc/nix/nix.custom.conf and restarts
# the daemon so it picks up the S3 substituter.
#
# Required environment variables (passed via Evergreen expansions):
#   mms_eng_test_aws_access_key  - AWS access key ID
#   mms_eng_test_aws_secret      - AWS secret access key
#
# Optional:
#   mms_eng_test_aws_region      - AWS region (default: us-east-1)
#
set -Eeou pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# --- Write AWS credentials for the Nix daemon (runs as root on Linux) ---
# Without this, the daemon cannot authenticate to the S3 cache bucket.

if [[ -n "${mms_eng_test_aws_access_key:-}" ]] && [[ -n "${mms_eng_test_aws_secret:-}" ]]; then
    echo "Configuring AWS credentials for Nix daemon..."
    sudo mkdir -p /root/.aws

    printf '[default]\naws_access_key_id = %s\naws_secret_access_key = %s\n' \
        "${mms_eng_test_aws_access_key}" "${mms_eng_test_aws_secret}" \
        | sudo tee /root/.aws/credentials > /dev/null

    printf '[default]\nregion = %s\n' "${mms_eng_test_aws_region:-us-east-1}" \
        | sudo tee /root/.aws/config > /dev/null

    sudo chmod 600 /root/.aws/credentials
    sudo chmod 600 /root/.aws/config
    echo "  AWS credentials written to /root/.aws/"
else
    echo "WARNING: AWS credentials not available (mms_eng_test_aws_access_key / mms_eng_test_aws_secret)."
    echo "  S3 Nix cache will be skipped; packages will download from cache.nixos.org."
fi

# --- Configure Nix to use the S3 cache and restart the daemon ---

"${SCRIPT_DIR}/configure-nix-cache.sh"
