#!/usr/bin/env bash

set -eou pipefail

script_name=$(readlink -f "${BASH_SOURCE[0]}")
script_dir=$(dirname "${script_name}")
repo_root=$(cd "${script_dir}/../../../.." && pwd)

source "${repo_root}/scripts/code_snippets/sample_test_runner.sh"

pushd "${script_dir}"

source env_variables.sh

prepare_snippets

run ra-13_0100_install_rustfs_s3.sh

popd
