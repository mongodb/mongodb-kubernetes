#!/usr/bin/env bash

# A script Evergreen will use to setup the evergreen CLI via go install.
#
# Go is available on Evg hosts at /opt/golang/go*/bin/go.
# The binary is installed into ${workdir}/bin so it lands on PATH.

set -Eeou pipefail

dest="${PROJECT_DIR:-${workdir}}/bin"
mkdir -p "${dest}"

echo "Installing evergreen CLI via go install..."
GOBIN="${dest}" go install github.com/evergreen-ci/evergreen/cmd/evergreen@latest
chmod +x "${dest}/evergreen"
echo "Installed evergreen to ${dest}"
