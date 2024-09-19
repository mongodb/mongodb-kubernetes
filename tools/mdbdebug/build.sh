#!/usr/bin/env bash

set -Eeou pipefail

script_name=$(readlink -f "${BASH_SOURCE[0]}")
script_dir=$(dirname "${script_name}")

TARGET_DIR=${TARGET_DIR:?}

pushd "${script_dir}" >/dev/null 2>&1
mkdir -p bin
mkdir -p bin_linux

echo "Building mdbdebug from $(pwd) directory"
go build -o bin ./...

echo "Building mdbdebug for linux from $(pwd) directory"
GOOS=linux GOARCH=amd64 go build -o bin_linux ./...

echo "Copying mdbdebug from $(pwd) to ${TARGET_DIR}/bin"
cp bin/mdbdebug "${TARGET_DIR}/bin"

echo "Copying attach.sh and watch.sh to ${TARGET_DIR}/bin"
cp attach.sh "${TARGET_DIR}/bin"

popd >/dev/null
