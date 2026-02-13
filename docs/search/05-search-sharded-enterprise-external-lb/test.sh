#!/usr/bin/env bash

set -eou pipefail

script_name=$(readlink -f "${BASH_SOURCE[0]}")
script_dir=$(dirname "${script_name}")
project_dir="${script_dir}/../../.."

source "${project_dir}/scripts/code_snippets/sample_test_runner.sh"

cd "${script_dir}"

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

# Deploy Envoy proxy for load balancing mongot traffic
run 05_0316_create_envoy_certificates.sh
run 05_0317_deploy_envoy_configmap.sh
run 05_0318_deploy_envoy.sh

run 05_0320_create_mongodb_search_resource.sh
run 05_0325_wait_for_search_resource.sh
run 05_0330_wait_for_sharded_cluster_ready.sh
run_for_output 05_0335_show_running_pods.sh
run_for_output 05_0340_verify_mongod_search_config.sh

# Create tools pod for running MongoDB commands
run 05_0342_run_mongodb_tools_pod.sh

run_for_output 05_0345_verify_mongos_search_config.sh

# Import sample data (includes sharding setup for movies and embedded_movies collections)
run_for_output 05_0350_import_sample_data.sh

# Create search indexes (text and vector)
run 05_0355_create_search_index_on_shards.sh
run 05_0356_create_vector_search_index.sh
run_for_output 05_0360_wait_for_search_index_ready.sh

# List search indexes to verify they are ready
run_for_output 05_0362_list_search_index.sh
run_for_output 05_0363_list_vector_search_index.sh

# Execute search queries through mongos and verify results
run_for_output 05_0365_execute_search_query_via_mongos.sh
run_for_output 05_0366_execute_vector_search_query_via_mongos.sh

cd -
