#!/usr/bin/env bash

set -eou pipefail

script_name=$(readlink -f "${BASH_SOURCE[0]}")
script_dir=$(dirname "${script_name}")

source "${script_dir}/../../../scripts/code_snippets/sample_test_runner.sh"

cd "${script_dir}"

prepare_snippets

# Setup: namespaces, secrets, operator
run 06_0040_validate_env.sh
run 06_0045_create_namespaces.sh
run 06_0046_create_image_pull_secrets.sh
run_for_output 06_0090_helm_add_mogodb_repo.sh
run_for_output 06_0100_install_operator.sh

# Create Ops Manager/Cloud Manager resources
run 06_0300_create_ops_manager_resources.sh

# TLS setup with cert-manager
run_for_output 06_0301_install_cert_manager.sh
run 06_0302_configure_tls_prerequisites.sh
run 06_0304_generate_tls_certificates.sh

# Create simulated external MongoDB sharded cluster (using Enterprise operator)
# Note: MongoDB is created WITHOUT search config initially
run 06_0310_create_external_mongodb_sharded_cluster.sh
run_for_output 06_0315_wait_for_external_cluster.sh
run 06_0305_create_external_mongodb_users.sh

# Deploy Envoy proxy for mongod-to-mongot traffic routing
run 06_0316_create_envoy_certificates.sh
run 06_0317_deploy_envoy_configmap.sh
run_for_output 06_0318_deploy_envoy.sh

# Create MongoDB Search resource with external sharded source
run 06_0320_create_mongodb_search_resource.sh
run_for_output 06_0325_wait_for_search_resource.sh

# Update MongoDB cluster with search config pointing to Envoy proxy
run 06_0326_update_mongodb_search_config.sh
run_for_output 06_0327_wait_for_mongodb_ready.sh

# Verify search configuration was applied correctly
run_for_output 06_0328_verify_mongod_search_config.sh
run_for_output 06_0329_verify_mongos_search_config.sh

run_for_output 06_0330_show_running_pods.sh

# Create tools pod for running MongoDB commands
run 06_0335_run_mongodb_tools_pod.sh

# Import sample data and shard collections
run_for_output 06_0340_import_sample_data.sh

# Create search indexes
run 06_0345_create_search_index.sh
run 06_0346_create_vector_search_index.sh
run_for_output 06_0350_wait_for_search_index_ready.sh
run_for_output 06_0351_wait_for_vector_search_index_ready.sh

# Execute search queries and verify results
run_for_output 06_0355_execute_search_query.sh
run_for_output 06_0356_execute_vector_search_query.sh

cd -
