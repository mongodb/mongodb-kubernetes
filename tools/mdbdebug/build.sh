#!/usr/bin/env bash

set -Eeou pipefail

script_name=$(readlink -f "${BASH_SOURCE[0]}")
script_dir=$(dirname "${script_name}")

pushd "${script_dir}" >/dev/null 2>&1
mkdir -p bin
mkdir -p bin_linux

echo "Building mdbdebug from $(pwd) directory"
go build -o bin ./...

echo "Building mdbdebug for linux from $(pwd) directory"
GOOS=linux GOARCH=amd64 go build -o bin_linux ./...

echo "Copying mdbdebug from to ${PROJECT_DIR}/bin"
cp bin/mdbdebug "${PROJECT_DIR}/bin"

echo "Copying attach.sh and watch.sh to ${PROJECT_DIR}/bin"
cp attach.sh "${PROJECT_DIR}/bin"

popd >/dev/null
