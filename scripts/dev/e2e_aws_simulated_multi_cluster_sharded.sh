#!/usr/bin/env bash
#
# Run the real-infra (AWS EKS) multi-cluster sharded search e2e against the four
# persistent EKS clusters in mongot_multicluster-infra (eu-south-1).
#
# Prerequisites:
#   - On the corp VPN (EKS API endpoints are corp-prefix-locked).
#   - AWS `mck-admin` profile resolvable (the kubeconfig exec uses it).
#   - Per-cluster SA bearer tokens extracted into MULTI_CLUSTER_CONFIG_DIR
#     (run the AWS prepare step first — see E2E_SCENARIO_PLAN.md gap #3).
#
# Usage: e2e_aws_simulated_multi_cluster_sharded.sh [-- <extra pytest args>]
#
# This targets persistent external infra, so it is NOT part of the kind-based
# e2e_run.sh flow and is NOT auto-run in the standard evergreen task group.

set -Eeou pipefail
test "${MDB_BASH_DEBUG:-0}" -eq 1 && set -x

cd "$(git rev-parse --show-toplevel 2>/dev/null || echo /workspace)"

# Load the AWS multi-cluster context (KUBECONFIG, MEMBER_CLUSTERS, CENTRAL_CLUSTER, etc.).
# shellcheck disable=SC1091
source scripts/dev/contexts/e2e_aws_simulated_mc_sharded

# shellcheck disable=SC1091
. scripts/dev/devenv

if [[ ! -d venv ]]; then
  echo "ERROR: venv not found at $(pwd)/venv. Run scripts/dev/recreate_python_venv.sh first." >&2
  exit 1
fi
# shellcheck disable=SC1091
source venv/bin/activate

if [[ ! -f "${MULTI_CLUSTER_CONFIG_DIR}/central_cluster" ]]; then
  echo "ERROR: ${MULTI_CLUSTER_CONFIG_DIR}/central_cluster missing — run the AWS token-extraction prepare step first." >&2
  exit 1
fi

extra_args=()
if [[ "${1:-}" == "--" ]]; then
  shift
  extra_args=("$@")
fi

mkdir -p logs
log_path="logs/test-e2e_aws_simulated_mc_sharded-$(date +%Y%m%d-%H%M%S).log"
ln -sf "${log_path#logs/}" logs/test.log

cd docker/mongodb-kubernetes-tests
pytest_args=(-v -s -m e2e_aws_simulated_mc_sharded)
if [[ ${#extra_args[@]} -gt 0 ]]; then
  pytest_args+=("${extra_args[@]}")
fi
echo "Running: pytest ${pytest_args[*]}"
exec pytest "${pytest_args[@]}" 2>&1 | tee "../../${log_path}"
