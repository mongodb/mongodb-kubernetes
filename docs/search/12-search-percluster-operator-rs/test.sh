#!/usr/bin/env bash
# Test runner script for MongoDB Search, operator-per-cluster with a unified CR,
# against a multi-cluster MongoDB replica set (MongoDBMultiCluster) source.
#
# This script executes all code snippets in order to test the full deployment flow.
# It assumes public/architectures/setup-multi-cluster/ra-01..ra-05 and
# public/architectures/ra-06-ops-manager-multi-cluster and
# public/architectures/ra-07-mongodb-replicaset-multi-cluster have already been run
# against the same 3 Kubernetes contexts -- see the README's Prerequisites section.
#
# Usage:
#   ./test.sh                    # Run with env_variables.sh
#   ./test.sh env_variables.sh   # Explicit env file

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

run 12_0040_validate_env.sh
run 12_0045_create_namespaces.sh
run 12_0046_create_image_pull_secrets.sh

# ============================================================================
# PER-CLUSTER SEARCH OPERATOR
# ============================================================================

run_for_output 12_0100_install_percluster_search_operator.sh
run_for_output 12_0110_stop_central_operator_watching_search.sh

# ============================================================================
# SOURCE CA CONFIGMAP (every member cluster trusts the source MongoDB's cert)
# ============================================================================

run 12_0303_create_source_ca_configmap.sh

# ============================================================================
# SYNC-SOURCE USER
# ============================================================================

run 12_0310_create_sync_source_user.sh

# ============================================================================
# SEARCH TLS CERTIFICATES
# ============================================================================

run 12_0316a_create_mongot_tls_certificate.sh
run 12_0316b_create_lb_tls_certificates.sh

# ============================================================================
# UNIFIED MONGODBSEARCH CR
# ============================================================================

run 12_0320_create_mongodb_search_resource.sh
run_for_output 12_0325_wait_for_search_resources.sh

# ============================================================================
# PER-CLUSTER MONGOTHOST (OPS MANAGER AUTOMATION CONFIG)
# ============================================================================

run_for_output 12_0400_configure_percluster_mongot_host.sh

# ============================================================================
# VERIFICATION
# ============================================================================

run_for_output 12_0410_verify_percluster_resources.sh

# ============================================================================
# FUNCTIONAL VERIFICATION (sample data, indexes, $search/$vectorSearch locality)
# ============================================================================

run 12_0500_create_search_admin_user.sh
run 12_0510_run_mongodb_tools_pods.sh
run 12_0520_insert_sample_data.sh
run_for_output 12_0530_create_search_indexes.sh
run_for_output 12_0540_query_search_percluster.sh
run 12_0550_internal_assert_search_results.sh

# ============================================================================
# DONE
# ============================================================================

echo ""
echo "============================================"
echo "MongoDB Search (operator-per-cluster) deployment complete!"
echo "============================================"
echo ""
echo "MongoDB Search is now running with:"
echo "  - ${SEARCH_MONGOT_REPLICAS} mongot replicas per cluster, across 3 clusters"
echo "  - A dedicated operator instance per cluster (no kubeconfig secrets)"
echo "  - Independent status per cluster"
echo ""
echo "To clean up, run: ./code_snippets/12_9010_delete_resources.sh"

cd -
