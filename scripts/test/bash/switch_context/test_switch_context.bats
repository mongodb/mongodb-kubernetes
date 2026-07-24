#!/usr/bin/env bats
#
# Regression: private_gke_code_snippets must source resolve-agent-version.sh
# before get_operator_helm_values() references ${AGENT_VERSION}.

@test "private_gke_code_snippets: resolves AGENT_VERSION before get_operator_helm_values" {
    # Source the real context file in a single subprocess with AGENT_VERSION
    # unset and the minimum Evergreen-like env that root-context requires.
    # Override source() to skip private-context (machine-local, outside the
    # behavior under test) and delegate every other source to the builtin.
    run bash -c '
        source() { [[ "$1" == */private-context ]] && return 0; builtin source "$@"; }
        export NAMESPACE="mongodb-test"
        export CLUSTER_NAME="kind"
        export LOCAL_OPERATOR="false"
        export REGISTRY="268558157000.dkr.ecr.us-east-1.amazonaws.com/staging"
        export K8S_CLUSTER_SUFFIX="bats-test"
        unset AGENT_VERSION
        source scripts/dev/contexts/private_gke_code_snippets
        echo "AGENT_VERSION=${AGENT_VERSION}"
        echo "MDB_GKE_PROJECT=${MDB_GKE_PROJECT}"
        echo "OPERATOR_ADDITIONAL_HELM_VALUES=${OPERATOR_ADDITIONAL_HELM_VALUES}"
    '
    [ "$status" -eq 0 ]

    # AGENT_VERSION was resolved from release.json via resolve-agent-version.sh
    grep -q '^AGENT_VERSION=.' <<<"$output"

    # MDB_GKE_PROJECT is set to the real GKE project
    grep -q '^MDB_GKE_PROJECT=.*scratch-kubernetes-team' <<<"$output"

    # OPERATOR_ADDITIONAL_HELM_VALUES contains a non-empty agent.version
    grep -q 'agent\.version=[^,"]' <<<"$output"
}
