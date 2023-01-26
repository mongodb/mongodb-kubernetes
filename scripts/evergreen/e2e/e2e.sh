#!/usr/bin/env bash
set -Eeou pipefail

start_time=$(date +%s)

if [[ -n "${KUBECONFIG:-}" && ! -f "${KUBECONFIG}" ]]; then
    echo "Kube configuration file does not exist!"
    exit 1
fi

source scripts/funcs/checks
source scripts/funcs/kubernetes
source scripts/funcs/printing
source scripts/evergreen/e2e/dump_diagnostic_information
source scripts/evergreen/e2e/lib

# Externally Configured Ops Manager (Cloud Manager)
# shellcheck source=~/.operator-dev/om
# shellcheck disable=SC1090
test -f "${OPS_MANAGER_ENV:-}" && source "${OPS_MANAGER_ENV}"

#
# This is the main entry point for running e2e tests. It can be used both for simple e2e tests (running a single test
# application) and for the Operator upgrade ones involving two steps (deploy previous Operator version, run test, deploy
# a new Operator - run verification tests)
# All the preparation work (fetching OM information, configuring resources) is done before running tests but
# it should be moved to e2e tests themselves (TODO)
#
check_env_var "TASK_NAME" "The 'TASK_NAME' must be specified for the Operator e2e tests"

# 1. Ensure the namespace exists - generate its name if not specified

if [[ -z "${PROJECT_NAMESPACE-}" ]]; then
    PROJECT_NAMESPACE=$(generate_random_namespace)
    export PROJECT_NAMESPACE

    echo "$PROJECT_NAMESPACE" > "${NAMESPACE_FILE}"
fi

ensure_namespace "${PROJECT_NAMESPACE}"

# 2. Fetch OM connection information - it will be saved to environment variables
# shellcheck disable=SC1091
. scripts/evergreen/e2e/fetch_om_information

# 3. Configure Operator resources
. scripts/evergreen/e2e/configure_operator.sh

export TEST_NAME="${TASK_NAME:?}"
delete_operator "${PROJECT_NAMESPACE}"

# 4. Main test run.

# We'll have the task running for the allocated time, minus the time it took us
# to get all the way here, assuming configuring and deploying the operator can
# take a bit of time. This is needed because Evergreen kills the process *AND*
# Docker containers running on the host when it hits a timeout. Under these
# circumstances and in Kind based environments, it is impossible to fetch the
# results from the Kubernetes cluster running on top of Docker.
#
current_time=$(date +%s)
elapsed_time=$((current_time - start_time))

task_timeout=$(get_timeout_for_task "${TASK_NAME:?}")

timeout_sec=$((task_timeout - elapsed_time - 60))
echo "This task is allowed to run for ${timeout_sec} seconds"
TEST_RESULTS=0
timeout --foreground "${timeout_sec}" scripts/evergreen/e2e/single_e2e.sh || TEST_RESULTS=$?
# Dump information from all clusters.
# TODO: ensure cluster name is included in log files so there is no overwriting of cross cluster files.
# shellcheck disable=SC2154
if [[ "${kube_environment_name:-}" = "multi" ]]; then
    echo "Dumping diagnostics for context ${central_cluster}"
    dump_all "${central_cluster}" || true

    for member_cluster in ${member_clusters}; do
      echo "Dumping diagnostics for context ${member_cluster}"
      dump_all "${member_cluster}" || true
    done
else
    # Dump all the information we can from this namespace
    dump_all || true
fi

if [[ "${TEST_RESULTS}" -ne 0 ]]; then
    # Mark namespace as failed to be cleaned later
    kubectl label "namespace/${PROJECT_NAMESPACE}" "evg/state=failed" --overwrite=true

    if [ "${always_remove_testing_namespace-}" = "true" ]; then
        # Failed namespaces might cascade into more failures if the namespaces
        # are not being removed soon enough.
        scripts/evergreen/e2e/teardown.sh "$(kubectl config current-context)" || true
    fi
else
    if [[ "${kube_environment_name}" = "multi" ]]; then
        echo "Tearing down cluster ${central_cluster}"
        scripts/evergreen/e2e/teardown.sh "${central_cluster}" || true

        for member_cluster in ${member_clusters}; do
            echo "Tearing down cluster ${member_cluster}"
            scripts/evergreen/e2e/teardown.sh "${member_cluster}" || true
        done
    else
        # If the test pass, then the namespace is removed
        scripts/evergreen/e2e/teardown.sh "$(kubectl config current-context)" || true
    fi
fi

# We exit with the test result to surface status to Evergreen.
exit ${TEST_RESULTS}
