#!/usr/bin/env bash

set -eou pipefail

script_name=$(readlink -f "${BASH_SOURCE[0]}")
script_dir=$(dirname "${script_name}")

source "${script_dir}/../../../scripts/code_snippets/sample_test_runner.sh"

cd "${script_dir}"

prepare_snippets

# --- Setup: operator + management Ops Manager ---
run 01_0040_validate_env.sh
run 01_0045_create_namespace.sh
run_for_output 01_0100_install_operator.sh
run 01_0200_create_om_admin_secret.sh
run 01_0210_deploy_management_om.sh
run_for_output 01_0215_wait_management_om.sh
run 01_0220_create_appdb_project_configmap.sh

# --- Fresh Start: external AppDB + primary OM referencing it ---
run 01_0300_create_appdb_mongodb.sh
run_for_output 01_0305_wait_appdb.sh
run 01_0310_create_primary_om.sh
run_for_output 01_0315_wait_primary_om.sh
run_for_output 01_0400_verify.sh

cd - >/dev/null
