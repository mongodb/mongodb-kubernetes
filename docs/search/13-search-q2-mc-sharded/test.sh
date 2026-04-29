# Test runner for Q2-MC Sharded — MongoDBSearch with managed Envoy + external sharded mongod.
#
# Usage:
#   source env_variables.sh
#   ./test.sh
#
# Prerequisites: 2-3 K8s clusters reachable via kubeconfig contexts and an external
# sharded cluster (mongos pool + per-shard tagged replica sets) per spec §6.1.

set -eou pipefail

script_name=$(readlink -f "${BASH_SOURCE[0]}")
script_dir=$(dirname "${script_name}")
project_dir="${script_dir}/../../.."

source "${project_dir}/scripts/code_snippets/sample_test_runner.sh"

cd "${script_dir}"

prepare_snippets

# Prerequisites
run               13_0040_validate_env.sh
run               13_0045_create_namespaces.sh
run_for_output    13_0050_kubectl_mongodb_multicluster_setup.sh
run_for_output    13_0100_install_operator.sh

# Customer-replicated secrets (load tester stages these per §6.3)
run               13_0200_verify_secrets_present.sh

# Apply CR + wait for steady-state across clusters/shards
run               13_0320_create_mongodb_search_resource.sh
run_for_output    13_0325_wait_for_search_resource.sh

# Verification
run_for_output    13_0326_verify_per_cluster_envoy.sh
run_for_output    13_0330_show_running_pods.sh
run_for_output    13_0340_query_through_envoy.sh

# Optional: shardOverrides example (commented out by default — uncomment to demo
# the per-shard `replicas` bias from spec §4.2 / §5.1.4)
# run               13_0322_apply_shard_overrides_example.sh
# run_for_output    13_0325_wait_for_search_resource.sh

echo ""
echo "============================================"
echo "Q2-MC Sharded deployment complete!"
echo "============================================"
echo ""
echo "MongoDBSearch is running across:"
echo "  - ${MEMBER_CLUSTER_0_NAME} (region=${MEMBER_CLUSTER_0_REGION})"
echo "  - ${MEMBER_CLUSTER_1_NAME} (region=${MEMBER_CLUSTER_1_REGION})"
echo "  - shards: ${MDB_SHARD_0_NAME}, ${MDB_SHARD_1_NAME}"
echo ""
echo "To clean up, run: ./code_snippets/13_9010_cleanup.sh"

cd -
