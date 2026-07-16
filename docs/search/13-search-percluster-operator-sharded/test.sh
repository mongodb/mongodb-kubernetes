#!/usr/bin/env bash
# Test runner script for MongoDB Search, operator-per-cluster model, against a
# single-cluster sharded MongoDB source shared by every Search cluster.
#
# This script executes all code snippets in order to test the full deployment flow.
# It can be run manually or as part of automated E2E testing.
#
# Usage:
#   ./test.sh                    # Run with env_variables.sh
#   ./test.sh env_variables.sh   # Explicit env file
#
# Prerequisites: ra-01..ra-05 (public/architectures/setup-multi-cluster/) must
# already be applied against the same two Kubernetes contexts referenced in
# env_variables.sh. This scenario deploys the sharded source itself -- it does
# not depend on ra-08.

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
run_for_output 13_0110_stop_central_operator_watching_search.sh

# ============================================================================
# TLS BOOTSTRAP + SHARDED SOURCE (cluster 0 only)
# ============================================================================

run_for_output 13_0201_install_cert_manager.sh
run 13_0202_configure_tls_prerequisites.sh
run 13_0203_create_ca_configmap.sh
run 13_0210_generate_source_tls_certificates.sh
run 13_0220_create_sharded_mongodb_source.sh
run_for_output 13_0225_wait_for_sharded_source.sh

# ============================================================================
# SYNC-SOURCE USER + CUSTOMER-REPLICATED SECRETS
# ============================================================================

run 13_0300_create_sync_source_user.sh
run 13_0301_replicate_sync_source_secret.sh

# ============================================================================
# TLS CERTIFICATES (per cluster, per shard) FOR MONGODBSEARCH
# ============================================================================

run 13_0310_create_mongot_tls_certificates.sh
run 13_0311_create_lb_tls_certificates.sh
run 13_0312_replicate_tls_secrets.sh

# ============================================================================
# MONGODBSEARCH -- SAME YAML APPLIED TO EVERY CLUSTER
# ============================================================================

run 13_0320_create_mongodb_search_resource.sh
run_for_output 13_0325_wait_for_search_resource.sh

# ============================================================================
# ROUTE THE SOURCE AT ONE SEARCH CLUSTER (OPS MANAGER AUTOMATION CONFIG)
# ============================================================================

run_for_output 13_0330_configure_om_automation_config.sh

# ============================================================================
# VERIFICATION
# ============================================================================

run_for_output 13_0335_verify_per_cluster_deployment.sh

# ============================================================================
# OPTIONAL: PER-SHARD SIZING OVERRIDE
# ============================================================================

run_for_output 13_0340_apply_shard_overrides.sh

cd - || true
