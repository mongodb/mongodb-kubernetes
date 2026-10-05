#!/usr/bin/env bash

set -eou pipefail

script_name=$(readlink -f "${BASH_SOURCE[0]}")
script_dir=$(dirname "${script_name}")

source "${script_dir}/../../../../scripts/code_snippets/sample_test_runner.sh"

cd "${script_dir}"

prepare_snippets

# --- Setup: multi-cluster operator + cert-manager + management Ops Manager ---
run eadb-mc-03_0040_validate_env.sh
run eadb-mc-03_0045_create_namespaces.sh
run_for_output eadb-mc-03_0100_install_operator.sh
run eadb-mc-03_0200_create_om_admin_secret.sh
run_for_output eadb-mc-03_0205_install_cert_manager.sh
run eadb-mc-03_0206_configure_tls_prerequisites.sh
run eadb-mc-03_0207_generate_appdb_certificate.sh
run eadb-mc-03_0210_deploy_management_om.sh
run_for_output eadb-mc-03_0215_wait_management_om.sh
run eadb-mc-03_0220_create_appdb_project_configmap.sh

# --- Reach the external-AppDB state (as in 01-mc-fresh-start) ---
run eadb-mc-03_0300_create_appdb_mongodbmulti.sh
run_for_output eadb-mc-03_0305_wait_appdb.sh
run eadb-mc-03_0310_create_primary_om.sh
run_for_output eadb-mc-03_0315_wait_primary_om.sh

# --- Reverse migration: external MongoDBMultiCluster -> internal multi-cluster AppDB ---
run eadb-mc-03_0320_reconfigure_to_internal.sh
run_for_output eadb-mc-03_0325_wait_release_and_adopt.sh
run eadb-mc-03_0330_delete_mongodbmulti_cr.sh
run_for_output eadb-mc-03_0400_verify.sh

cd - >/dev/null
