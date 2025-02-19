#!/usr/bin/env bash
set -Eeou pipefail

# Set required version
required_version="v1.61.0"

# Install or update golangci-lint if not installed or version is incorrect
if ! [[ -x "$(command -v golangci-lint)" ]]; then
    echo "Installing/updating golangci-lint to version ${required_version}..."
    curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b "$(go env GOPATH)"/bin "${required_version}"
else
    echo "golangci-lint is already installed"
fi

echo "Go Version: $(go version)"

echo "Running golangci-lint..."
if PATH=$(go env GOPATH)/bin:${PATH} golangci-lint run --fix; then
    echo "No issues found by golangci-lint."
else
    echo "golangci-lint found issues or made changes."
    exit 1
fi
