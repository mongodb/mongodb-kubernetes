#!/usr/bin/env bash
# Test runner script for MongoDB Search with External Sharded MongoDB + Managed Envoy LB
#
# This script executes all code snippets in order to test the full deployment flow.
# It can be run manually or as part of automated E2E testing.
#
# Usage:
#   ./test.sh                    # Run with env_variables.sh
#   ./test.sh env_variables.sh   # Explicit env file
#
# For E2E testing, env_variables_e2e_private.sh is sourced automatically
# when PROJECT_DIR and CLUSTER_NAME environment variables are set.

set -eou pipefail

script_name=$(readlink -f "${BASH_SOURCE[0]}")
script_dir=$(dirname "${script_name}")
project_dir="${script_dir}/../../.."

source "${project_dir}/scripts/code_snippets/sample_test_runner.sh"

cd "${script_dir}"

prepare_snippets

# ============================================================================
# PREREQUISITES
# ============================================================================

run 07_0040_validate_env.sh
run 07_0045_create_namespaces.sh
run 07_0046_create_image_pull_secrets.sh

run_for_output 07_0090_helm_add_mongodb_repo.sh
run_for_output 07_0100_install_operator.sh

# Ops Manager resources (for simulated external cluster)
run 07_0300_create_ops_manager_resources.sh

# ============================================================================
# TLS CONFIGURATION
# ============================================================================

run_for_output 07_0301_install_cert_manager.sh
run 07_0302_configure_tls_prerequisites.sh
run 07_0304_generate_tls_certificates.sh

# ============================================================================
# SIMULATED EXTERNAL MONGODB SHARDED CLUSTER
# ============================================================================

# Create simulated external MongoDB sharded cluster (using Enterprise operator)
# Note: MongoDB is created WITH search config from the start (pointing to Envoy proxy endpoints)
run 07_0310_create_external_mongodb_sharded_cluster.sh
run_for_output 07_0315_wait_for_external_cluster.sh

# Create users AFTER cluster is ready (MongoDBUser CRDs reference the cluster)
run 07_0305_create_external_mongodb_users.sh

# ============================================================================
# MONGODB SEARCH WITH MANAGED ENVOY LB
# ============================================================================

# Create TLS certificates for mongot
run 07_0316_create_search_tls_certificates.sh

# Create MongoDBSearch with lb.mode: Managed
# NOTE: No Envoy deployment script - the operator handles this automatically!
run 07_0320_create_mongodb_search_resource.sh
run_for_output 07_0325_wait_for_search_resource.sh

# ============================================================================
# VERIFICATION
# ============================================================================

# Verify operator-managed Envoy is deployed
run_for_output 07_0326_verify_envoy_deployment.sh

# Show all running pods
run_for_output 07_0330_show_running_pods.sh

# Deploy tools pod for MongoDB commands
run 07_0335_run_mongodb_tools_pod.sh

# Wait for search configuration to propagate to mongod/mongos
# The automation agent needs time to apply the config changes
echo "Waiting 60s for search configuration to propagate to mongod/mongos..."
sleep 60

# TODO: Re-enable verification once scripts are updated to read from config files
# The current scripts use getParameter which doesn't work for startup params
# Python E2E tests read from /data/automation-mongod.conf instead
# run_for_output 07_0328_verify_mongod_search_config.sh
# run_for_output 07_0329_verify_mongos_search_config.sh

# ============================================================================
# DATA IMPORT AND SEARCH TESTING
# ============================================================================

# Import sample data and shard collections
run_for_output 07_0340_import_sample_data.sh

# Create search indexes
run 07_0345_create_search_index.sh
run 07_0346_create_vector_search_index.sh
run_for_output 07_0350_wait_for_search_indexes.sh

# Execute search queries
run_for_output 07_0355_execute_search_query.sh
run_for_output 07_0356_execute_vector_search_query.sh

# ============================================================================
# DONE
# ============================================================================

echo ""
echo "============================================"
echo "✓ All snippets executed successfully!"
echo "============================================"
echo ""
echo "MongoDB Search is now running with:"
echo "  - External sharded MongoDB source (simulated)"
echo "  - Managed Envoy L7 load balancer (operator-deployed)"
echo "  - ${MDB_SHARD_COUNT} shards with ${MDB_MONGOT_REPLICAS:-1} mongot replicas each"
echo ""
echo "To clean up, run: ./code_snippets/07_9010_delete_namespace.sh"

cd -
