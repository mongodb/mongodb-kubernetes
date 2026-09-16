#!/usr/bin/env bash

set -eou pipefail

script_name=$(readlink -f "${BASH_SOURCE[0]}")
script_dir=$(dirname "${script_name}")

source "${script_dir}/../../../scripts/code_snippets/sample_test_runner.sh"

cd "${script_dir}"

prepare_snippets

# --- Setup: operator + management Ops Manager ---
run eadb-02_0040_validate_env.sh
run eadb-02_0045_create_namespace.sh
run_for_output eadb-02_0100_install_operator.sh
run eadb-02_0200_create_om_admin_secret.sh
run eadb-02_0210_deploy_management_om.sh
run_for_output eadb-02_0215_wait_management_om.sh
run eadb-02_0220_create_appdb_project_configmap.sh

# --- Forward migration: internal AppDB -> external MongoDB CR ---
run eadb-02_0300_create_primary_om_internal.sh
run_for_output eadb-02_0305_wait_internal_appdb.sh
run eadb-02_0310_create_appdb_mongodb.sh
run_for_output eadb-02_0315_wait_appdb_pending.sh
run eadb-02_0320_set_external_ref.sh
run_for_output eadb-02_0325_wait_after_switch.sh
run_for_output eadb-02_0400_verify.sh

cd - >/dev/null
