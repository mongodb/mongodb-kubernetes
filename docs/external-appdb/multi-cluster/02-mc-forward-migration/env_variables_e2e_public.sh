# E2E Test Environment - Public Configuration
#
# Sources the default environment and overrides CI-specific values for public testing.

source "$(dirname "${BASH_SOURCE[0]}")/env_variables.sh"

# Central cluster hosts the operator and both Ops Managers; the two member
# clusters host the AppDB data pods.
export K8S_CTX_0="kind-e2e-cluster-1"
export K8S_CTX_1="kind-e2e-cluster-2"
export K8S_CTX_2="kind-e2e-cluster-3"

source "$(dirname "${BASH_SOURCE[0]}")/env_variables_e2e_common.sh"
