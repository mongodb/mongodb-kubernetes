#!/usr/bin/env bash

set -eou pipefail

script_name=$(readlink -f "${BASH_SOURCE[0]}")
script_dir=$(dirname "${script_name}")

source "${script_dir}/../../../scripts/code_snippets/sample_test_runner.sh"

cd "${script_dir}"

prepare_snippets

# --- Setup: operator + management Ops Manager ---
run 03_0040_validate_env.sh
run 03_0045_create_namespace.sh
run_for_output 03_0100_install_operator.sh
run 03_0200_create_om_admin_secret.sh
run 03_0210_deploy_management_om.sh
run_for_output 03_0215_wait_management_om.sh
run 03_0220_create_appdb_project_configmap.sh

# --- Reach the external-AppDB state (as in 01-fresh-start) ---
run 03_0300_create_appdb_mongodb.sh
run_for_output 03_0305_wait_appdb.sh
run 03_0310_create_primary_om.sh
run_for_output 03_0315_wait_primary_om.sh

# --- Reverse migration: external MongoDB CR -> internal AppDB ---
run 03_0320_reconfigure_to_internal.sh
run_for_output 03_0325_wait_release_and_adopt.sh
run 03_0330_delete_mongodb_cr.sh
run_for_output 03_0400_verify.sh

cd - >/dev/null
