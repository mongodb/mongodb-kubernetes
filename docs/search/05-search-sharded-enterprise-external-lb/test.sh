#!/usr/bin/env bash

set -eou pipefail

script_name=$(readlink -f "${BASH_SOURCE[0]}")
script_dir=$(dirname "${script_name}")
project_dir="${script_dir}/../../.."

source "${project_dir}/scripts/code_snippets/sample_test_runner.sh"

cd "${script_dir}"

## Source environment variables
## First source the base env_variables.sh
#source "${script_dir}/env_variables.sh"
#
## Then source flavor-specific overrides if CODE_SNIPPETS_FLAVOR is set
#CODE_SNIPPETS_FLAVOR="${CODE_SNIPPETS_FLAVOR:-}"
#if [[ -n "${CODE_SNIPPETS_FLAVOR}" ]]; then
#  flavor_env_file="${script_dir}/env_variables_${CODE_SNIPPETS_FLAVOR}.sh"
#  if [[ -f "${flavor_env_file}" ]]; then
#    echo "Sourcing env variables for ${CODE_SNIPPETS_FLAVOR} flavor"
#    export PROJECT_DIR="${project_dir}"
#    source "${flavor_env_file}"
#  fi
#fi

prepare_snippets

run 05_0040_validate_env.sh
run 05_0045_create_namespaces.sh
run 05_0046_create_image_pull_secrets.sh

run_for_output 05_0090_helm_add_mogodb_repo.sh
run_for_output 05_0100_install_operator.sh
run 05_0300_create_ops_manager_resources.sh

# TLS setup
run_for_output 05_0301_install_cert_manager.sh
run 05_0302_configure_tls_prerequisites.sh
run 05_0304_generate_tls_certificates.sh

run 05_0305_create_mongodb_sharded_cluster.sh
run_for_output 05_0310_wait_for_sharded_cluster.sh
run 05_0315_create_mongodb_users.sh
run 05_0320_create_mongodb_search_resource.sh
run 05_0325_wait_for_search_resource.sh
run 05_0330_wait_for_sharded_cluster_ready.sh
run_for_output 05_0335_show_running_pods.sh
run_for_output 05_0340_verify_mongod_search_config.sh
run_for_output 05_0345_verify_mongos_search_config.sh

# Import sample data and create search indexes
run 05_0350_import_sample_data.sh
run 05_0355_create_search_index_on_shards.sh
run_for_output 05_0360_wait_for_search_index_ready.sh

# Execute search queries through mongos and verify results
run_for_output 05_0365_execute_search_query_via_mongos.sh
run_for_output 05_0370_verify_search_results_from_all_shards.sh

cd -
