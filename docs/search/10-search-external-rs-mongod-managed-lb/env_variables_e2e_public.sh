# E2E Test Environment - Public Configuration
#
# Sources the default environment and overrides
# CI-specific values for public testing.

source "$(dirname "${BASH_SOURCE[0]}")/env_variables.sh"

export K8S_CTX="kind-kind"
