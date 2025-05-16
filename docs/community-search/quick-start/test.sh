#!/usr/bin/env bash

set -eou pipefail

script_name=$(readlink -f "${BASH_SOURCE[0]}")
script_dir=$(dirname "${script_name}")

source scripts/code_snippets/sample_test_runner.sh

cd "${script_dir}"

prepare_snippets

run 0045_create_namespaces.sh
run 0046_create_image_pull_secrets.sh

run_for_output 090_helm_add_mogodb_repo.sh
run_for_output 0100_install_operator.sh
run_for_output 0200_configure_community_search_pullsecret.sh
run_for_output 0210_verify_community_search_pullsecret.sh
run 0305_create_mongodb_community_user_secrets.sh
run 0310_create_mongodb_community_resource.sh
run 0315_wait_for_community_resource.sh
run 0320_create_mongodb_search_resource.sh
run 0325_wait_for_search_resource.sh
run 0330_wait_for_community_resource.sh
run_for_output 0335_show_running_pods.sh
run 0410_run_mongodb_tools_pod.sh
run 0420_import_movies_mflix_database.sh
run 0430_create_search_index.sh
run_for_output 0440_wait_for_search_index_ready.sh
run_for_output 0450_execute_search_query.sh
cd -
