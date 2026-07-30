#!/usr/bin/env bash

set -eou pipefail

script_name=$(readlink -f "${BASH_SOURCE[0]}")
script_dir=$(dirname "${script_name}")

source scripts/code_snippets/sample_test_runner.sh

pushd "${script_dir}"

# Source env_variables.sh so gcloud_retry() is available in this process.
# The parent test runner also sources this, but functions aren't inherited
# by subprocesses — only exported variables are.
source env_variables.sh

prepare_snippets

run_for_output ra-05_0215_helm_configure_repo.sh
run_for_output ra-05_0216_helm_install_cert_manager.sh
run ra-05_0220_create_issuer.sh
run_for_output ra-05_0221_verify_issuer.sh
run ra-05_0225_create_ca_configmap.sh

popd
