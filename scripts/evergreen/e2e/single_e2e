#!/usr/bin/env bash

set -euo pipefail

##
## The script deploys a single test application and waits until it finishes.
## All the Operator deployment, configuration and teardown work is done in 'e2e' script
## TODO rename the file to 'e2e_single_test.sh' or whatever and move to 'e2e' directory
##

cd "$(git rev-parse --show-toplevel || echo "Failed to find git root"; exit 1)"
source scripts/funcs

check_env_var "TEST_NAME" "The 'TEST_NAME' must be specified to run the Operator single e2e test"

deploy_test_app() {
    title "Deploying test application"

    GIT_SHA=$(git rev-parse HEAD)

    # If running in evergreen, prefer the VERSION_ID to avoid GIT_SHA collisions
    # when people is building images from same commit.

    # Set the test_image_tag outside this function
    # TEST_IMAGE_TAG="${VERSION_ID:-$GIT_SHA}"
    [[ -z "${TEST_IMAGE_TAG-}" ]] && TEST_IMAGE_TAG="${OPERATOR_VERSION:-$GIT_SHA}"

    # apply the correct configuration of the running OM instance
    # note, that the 4 last parameters are used only for Mongodb resource testing - not for Ops Manager
    helm template "scripts/evergreen/deployments/test-app" \
         --set repo="${REPO_URL:=268558157000.dkr.ecr.us-east-1.amazonaws.com/dev}" \
         --set namespace="${PROJECT_NAMESPACE}" \
         --set taskName="${task_name}" \
         --set pytest.addopts="${pytest_addopts}" \
         --set operator.name="${OPERATOR_NAME:=mongodb-enterprise-operator}" \
         --set managedSecurityContext="${MANAGED_SECURITY_CONTEXT:=false}" \
         --set tag="${TEST_IMAGE_TAG}" \
         --set aws.accessKey="${AWS_ACCESS_KEY_ID:=}" \
         --set aws.secretAccessKey="${AWS_SECRET_ACCESS_KEY:=}" \
         --set skipExecution="${SKIP_EXECUTION:="'false'"}" \
         --set baseUrl="${OM_BASE_URL:=http://ops-manager.${OPS_MANAGER_NAMESPACE}.svc.cluster.local:8080}" \
         --set apiKey="${OM_API_KEY:-}" \
         --set apiUser="${OM_USER:=admin}" \
         --set orgId="${OM_ORGID:-}"  > mongodb-enterprise-tests.yaml || exit 1

    kubectl -n "${PROJECT_NAMESPACE}" delete -f mongodb-enterprise-tests.yaml 2>/dev/null  || true

    kubectl -n "${PROJECT_NAMESPACE}" apply -f mongodb-enterprise-tests.yaml

    rm mongodb-enterprise-tests.yaml
}

wait_until_pod_is_running_or_failed_or_succeeded() {
    # Do wait while the Pod is not yet running or failed (can be in Pending or ContainerCreating state)
    # Note that the pod may jump to Failed/Completed state quickly - so we need to give up waiting on this as well
    echo "Waiting until the test application gets to Running state..."

    while ! test_app_running_or_ended; do printf .; sleep 1; done;
    echo
}

test_app_running_or_ended() {
    status="$(kubectl -n "${PROJECT_NAMESPACE}" get pod "${TEST_APP_PODNAME}" -o jsonpath="{.status.phase}")"
    [[ "${status}" = "Running" || "${status}" = "Failed" || "${status}" = "Succeeded" ]]
}

test_app_ended() {
    status="$(kubectl -n "${PROJECT_NAMESPACE}" get pod "${TEST_APP_PODNAME}" -o jsonpath="{.status.phase}")"
    [[ "${status}" = "Failed" || "${status}" = "Succeeded" ]]
}

# Will run the test application and wait for its completion.
run_tests() {
    task_name=${1}
    timeout=${2}

    TEST_APP_PODNAME=mongodb-enterprise-operator-tests

    deploy_test_app

    wait_until_pod_is_running_or_failed_or_succeeded

    title "Running e2e test ${task_name} (tag: ${TEST_IMAGE_TAG})"

    # we don't output logs to file when running tests locally
    if [[ "${MODE-}" == "dev" ]]; then
        kubectl -n "${PROJECT_NAMESPACE}" logs "${TEST_APP_PODNAME}" -f
    else
        output_filename="logs/test_app.log"
        operator_filename="logs/operator.log"

        # At this time both ${TEST_APP_PODNAME} have finished running, so we don't follow (-f) it
        # Similarly, the operator deployment has finished with our tests, so we print whatever we have
        # until this moment and go continue with our lives
        kubectl -n "${PROJECT_NAMESPACE}" logs "${TEST_APP_PODNAME}" -f | tee "${output_filename}"
        kubectl -n "${PROJECT_NAMESPACE}" logs "deployment/mongodb-enterprise-operator" > "${operator_filename}"
    fi

    # Waiting a bit until the pod gets to some end
    while ! test_app_ended; do printf .; sleep 1; done;
    echo

    [[ $(kubectl -n "${PROJECT_NAMESPACE}" get pods/${TEST_APP_PODNAME} -o jsonpath='{.status.phase}') == "Succeeded" ]]
}

mkdir -p logs/

TESTS_OK=0
run_tests "${TEST_NAME}" "${WAIT_TIMEOUT:-400}" || TESTS_OK=1

echo "Tests have finished with the following exit code: ${TESTS_OK}"

[[ "${TESTS_OK}" -eq 0 ]]
