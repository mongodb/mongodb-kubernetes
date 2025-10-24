#!/usr/bin/env bash

set -Eeou pipefail

script_name=$(readlink -f "${BASH_SOURCE[0]}")
script_dir=$(dirname "${script_name}")

pushd "${script_dir}" >/dev/null 2>&1
mkdir bin bin_linux >/dev/null 2>&1 || true

echo "Building diffwatch from $(pwd) directory"
GOOS=linux GOARCH=amd64 go build -o bin_linux ./...
go build -o bin ./...

echo "Copying diffwatch from $(pwd) to ${PROJECT_DIR}/bin"
cp bin/diffwatch "${PROJECT_DIR}"/bin
