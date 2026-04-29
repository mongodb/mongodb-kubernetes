# Test runner for Q2-MC ReplicaSet — MongoDBSearch with managed Envoy + external mongod.
#
# Usage:
#   source env_variables.sh
#   ./test.sh
#
# Prerequisites: 2-3 K8s clusters reachable via kubeconfig contexts and an external
# replica set (with regional tags) per spec §6.1.

set -eou pipefail

script_name=$(readlink -f "${BASH_SOURCE[0]}")
script_dir=$(dirname "${script_name}")
project_dir="${script_dir}/../../.."

source "${project_dir}/scripts/code_snippets/sample_test_runner.sh"

cd "${script_dir}"

prepare_snippets

# Prerequisites
run               12_0040_validate_env.sh
run               12_0045_create_namespaces.sh
run_for_output    12_0050_kubectl_mongodb_multicluster_setup.sh
run_for_output    12_0100_install_operator.sh

# Customer-replicated secrets (load tester stages these per §6.3 — verified, not created)
run               12_0200_verify_secrets_present.sh

# Apply CR + wait for steady-state across clusters
run               12_0320_create_mongodb_search_resource.sh
run_for_output    12_0325_wait_for_search_resource.sh

# Verification
run_for_output    12_0326_verify_per_cluster_envoy.sh
run_for_output    12_0330_show_running_pods.sh
run_for_output    12_0340_query_through_envoy.sh

echo ""
echo "============================================"
echo "Q2-MC RS deployment complete!"
echo "============================================"
echo ""
echo "MongoDBSearch is running across:"
echo "  - ${MEMBER_CLUSTER_0_NAME} (region=${MEMBER_CLUSTER_0_REGION})"
echo "  - ${MEMBER_CLUSTER_1_NAME} (region=${MEMBER_CLUSTER_1_REGION})"
echo ""
echo "To clean up, run: ./code_snippets/12_9010_cleanup.sh"

cd -
