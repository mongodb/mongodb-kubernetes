# One-stop environment for this scenario: source THIS file (don't execute it)
# and it performs the whole sourcing dance in the right order -- the
# prerequisite reference-architecture (ra-*) env files, then the Search
# version floors, then this scenario's own env_variables.sh.
#
#   source env.sh
#
# Not on GKE? Export your three contexts BEFORE sourcing, e.g.:
#   export K8S_CLUSTER_0_CONTEXT_NAME=kind-e2e-cluster-1
#   export K8S_CLUSTER_1_CONTEXT_NAME=kind-e2e-cluster-2
#   export K8S_CLUSTER_2_CONTEXT_NAME=kind-e2e-cluster-3
# When they're already set, the GKE context derivation (ra-01) is skipped.
#
# Re-source this file in every new shell before running any snippet -- the
# environment lives in your shell, not on disk. Every snippet checks its own
# variables and tells you if you forgot.

_scenario_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
_arch_dir="${_scenario_dir}/../../../public/architectures"

# ra-01 only derives GKE context names; skip it when contexts are already set.
if [[ -z "${K8S_CLUSTER_0_CONTEXT_NAME:-}" ]]; then
  source "${_arch_dir}/setup-multi-cluster/ra-01-setup-gke/env_variables.sh"
fi

source "${_arch_dir}/setup-multi-cluster/ra-02-setup-operator/env_variables.sh"
source "${_arch_dir}/ra-06-ops-manager-multi-cluster/env_variables.sh"
source "${_arch_dir}/ra-07-mongodb-replicaset-multi-cluster/env_variables.sh"

# Search version floors -- AFTER the ra files, which hard-set older defaults
# (exporting these any earlier gets silently overwritten). Edit if you need
# different versions; see README "Versions: floors, not pins".
export OPS_MANAGER_VERSION="8.0.25"
export MONGODB_VERSION="8.3.4-ent"

source "${_scenario_dir}/env_variables.sh"

unset _scenario_dir _arch_dir
echo "[ok] environment loaded: contexts ${K8S_CLUSTER_0_CONTEXT_NAME} / ${K8S_CLUSTER_1_CONTEXT_NAME} / ${K8S_CLUSTER_2_CONTEXT_NAME}, namespace ${MDB_NAMESPACE}"
