#!/usr/bin/env bash

set -Eeou pipefail

script_name=$(readlink -f "${BASH_SOURCE[0]}")
script_dir=$(dirname "${script_name}")

pushd "${script_dir}" >/dev/null 2>&1
mkdir -p bin_darwin_arm64 bin_linux_amd64 bin_linux_arm64 >/dev/null 2>&1 || true

echo "Building diffwatch from $(pwd) directory"

# Build for Linux AMD64
echo "Building for linux/amd64..."
GOOS=linux GOARCH=amd64 go build -o bin_linux_amd64 ./...

# Build for Linux ARM64 (for Docker on Mac ARM64)
echo "Building for linux/arm64..."
GOOS=linux GOARCH=arm64 go build -o bin_linux_arm64 ./...

# Build for Darwin ARM64 (Mac native)
echo "Building for darwin/arm64..."
GOOS=darwin GOARCH=arm64 go build -o bin_darwin_arm64 ./...

echo "Copying diffwatch from $(pwd) to ${PROJECT_DIR}/bin"
cp bin_darwin_arm64/diffwatch "${PROJECT_DIR}"/bin
