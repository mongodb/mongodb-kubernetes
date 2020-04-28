#!/usr/bin/env bash
set -Eeou pipefail

start_time=$(date +%s)

if [[ -n "${KUBECONFIG:-}" && ! -f "${KUBECONFIG}" ]]; then
    echo "Kube configuration file does not exist!"
    exit 1
fi

set -euo pipefail
cd "$(git rev-parse --show-toplevel || echo "Failed to find git root"; exit 1)" || exit 1

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
. scripts/evergreen/e2e/configure_operator

export TEST_NAME="${TASK_NAME:?}"
delete_operator "${PROJECT_NAMESPACE}"

# 4. (optionally) Preliminary step in the case of Operator upgrade
if echo "${TASK_NAME}" | grep -E -q "^e2e_op_upgrade"; then
    export TEST_NAME="${TASK_NAME}_first"
    header "Performing the first stage (${TEST_NAME}) of an Operator upgrade test"

    # We need to checkout the latest (or a specific) release in a separate directory and install
    # Operator from there
    tmp_dir=$(mktemp -d)
    pushd "${tmp_dir}"

    checkout_latest_official_branch

    # TODO add support for quay.io UBI images as well
    # FIXME: Registry overridden for ugrade (operator, ops manager, init ops
    # manager), need to check if we can avoid quay?
    # FIXME: once quay has init images will be updated
    # If the version of the Operator to upgrade from not specified then we
    # assume it's the latest
    if ! deploy_operator \
        "quay.io/mongodb" \
        "${INIT_OPS_MANAGER_REGISTRY}" \
        "${INIT_APPDB_REGISTRY:?}" \
        "${PROJECT_NAMESPACE}" \
        "${OPERATOR_VERSION_UPGRADE_FROM:-"latest"}" \
        "${WATCH_NAMESPACE:-$PROJECT_NAMESPACE}" \
        "Always" \
        "${MANAGED_SECURITY_CONTEXT:-}" \
        "2m"
    then
        echo "Operator failed to start"
        exit 1
    fi

    rm -rf "${tmp_dir}"
    popd > /dev/null || return

    # Running test
    if ! ./scripts/evergreen/e2e/single_e2e; then
        dump_all
        scripts/evergreen/e2e/teardown
        exit 1
    fi
    # Setting the second test to be run after the Operator upgrade after 'if' block
    # Note, that in this case the second Operator will be upgraded, not deleted-created
    export TEST_NAME="${TASK_NAME}_second"
    header "Performing the second stage (${TEST_NAME})"
fi

# 5. Main test run. In case of Operator upgrade this will be the second test run and
# the Operator won't be removed - only upgraded

if ! deploy_operator \
    "${REGISTRY}" \
    "${INIT_OPS_MANAGER_REGISTRY}" \
    "${INIT_APPDB_REGISTRY}" \
    "${PROJECT_NAMESPACE}" \
    "${version_id:?}" \
    "${WATCH_NAMESPACE:-$PROJECT_NAMESPACE}" \
    "Always" \
    "${MANAGED_SECURITY_CONTEXT:-}" \
    "2m"
then
    echo "Operator failed to start"
    exit 1
fi


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
timeout --foreground "${timeout_sec}" scripts/evergreen/e2e/single_e2e || TEST_RESULTS=$?

# Dump all the information we can from this namespace
dump_all

if [[ "${TEST_RESULTS}" -ne 0 ]]; then
    # Mark namespace as failed to be cleaned later
    kubectl label "namespace/${PROJECT_NAMESPACE}" "evg/state=failed" --overwrite=true

    if [ "${always_remove_testing_namespace-}" = "true" ]; then
        # Failed namespaces might cascade into more failures if the namespaces
        # are not being removed soon enough.
        scripts/evergreen/e2e/teardown
    fi
else
    # If the test pass, then the namespace is removed
    scripts/evergreen/e2e/teardown
fi

# We exit with the test result to surface status to Evergreen.
exit $TEST_RESULTS
