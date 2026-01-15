#!/usr/bin/env bash

set -eou pipefail

script_name=$(readlink -f "${BASH_SOURCE[0]}")
script_dir=$(dirname "${script_name}")

source "${script_dir}/../../../scripts/code_snippets/sample_test_runner.sh"

cd "${script_dir}"

prepare_snippets

run 05_0040_validate_env.sh
run 05_0045_create_namespaces.sh
run 05_0046_create_image_pull_secrets.sh

run_for_output 05_0090_helm_add_mogodb_repo.sh
run_for_output 05_0100_install_operator.sh
run 05_0300_create_ops_manager_resources.sh

run 05_0305_create_mongodb_sharded_cluster.sh
run_for_output 05_0310_wait_for_sharded_cluster.sh
run 05_0315_create_mongodb_users.sh
run 05_0320_create_mongodb_search_resource.sh
run 05_0325_wait_for_search_resource.sh
run 05_0330_wait_for_sharded_cluster_ready.sh
run_for_output 05_0335_show_running_pods.sh
run_for_output 05_0340_verify_mongod_search_config.sh

cd -

