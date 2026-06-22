#!/usr/bin/env bash
# Test runner script for MongoDB Search with a multi-cluster (topology:
# MultiCluster) sharded MongoDB source + per-cluster managed Envoy LB
#
# This script executes all code snippets in order to test the full deployment flow.
# It can be run manually or as part of automated E2E testing.
#
# Usage:
#   ./test.sh                    # Run with env_variables.sh
#
# For E2E testing, env_variables_e2e_private.sh is sourced automatically
# by the test runner.

script_name=$(readlink -f "${BASH_SOURCE[0]}")
script_dir=$(dirname "${script_name}")
project_dir="${script_dir}/../../.."

source "${project_dir}/scripts/code_snippets/sample_test_runner.sh"

cd "${script_dir}" || exit 1

prepare_snippets

# ============================================================================
# PREREQUISITES
# ============================================================================

run 13_0040_validate_env.sh
run 13_0045_create_namespaces.sh
run 13_0046_create_image_pull_secrets.sh

run_for_output 13_0100_install_operator.sh

# Ops Manager resources (for the operator-managed cluster)
run 13_0300_create_ops_manager_resources.sh

# ============================================================================
# TLS CONFIGURATION
# ============================================================================

run_for_output 13_0301_install_cert_manager.sh
run 13_0302_configure_tls_prerequisites.sh
run 13_0302a_configure_tls_prerequisites_mongod.sh
run 13_0302b_configure_tls_prerequisites_mongot.sh
run 13_0304_generate_tls_certificates.sh

# ============================================================================
# OPERATOR-MANAGED MULTI-CLUSTER MONGODB SHARDED CLUSTER
# ============================================================================

# Create the multi-cluster sharded cluster (search setParameters are configured
# per shard / for mongos -- there is no auto-wiring on multi-cluster)
run 13_0310_create_mongodb_mc_sharded.sh
run_for_output 13_0315_wait_for_sharded_cluster.sh

# Create users AFTER the cluster is ready (MongoDBUser CRDs reference it)
run 13_0316_create_mongodb_users.sh

# ============================================================================
# MONGODB SEARCH WITH PER-CLUSTER MANAGED ENVOY LB
# ============================================================================

# Create TLS certificates for the per-(cluster,shard) mongot pods
run 13_0316a_create_mongot_tls_certificates.sh

# Create TLS certificates for the managed load balancer (Envoy)
run 13_0316b_create_lb_tls_certificates.sh

# Replicate the Search Secrets to the member clusters (the operator does not)
run 13_0317_replicate_search_secrets.sh

# Create MongoDBSearch with the external multi-cluster sharded source
run 13_0320_create_mongodb_search_resource.sh
run_for_output 13_0325_wait_for_search_resource.sh

# ============================================================================
# VERIFICATION
# ============================================================================

# Verify the per-(cluster,shard) mongot StatefulSets and operator-managed Envoys
run_for_output 13_0326_internal_verify_envoy_deployment.sh

# Show all running pods
run_for_output 13_0330_show_running_pods.sh

cd - || true
