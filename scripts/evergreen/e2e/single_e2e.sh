#!/usr/bin/env bash

set -Eeou pipefail

##
## The script deploys a single test application and waits until it finishes.
## All the Operator deployment, configuration and teardown work is done in 'e2e' script
##

source scripts/funcs/checks
source scripts/funcs/printing
source scripts/funcs/errors

check_env_var "TEST_NAME" "The 'TEST_NAME' must be specified to run the Operator single e2e test"

if [[ "${IMAGE_TYPE}" = "ubi" ]]; then
    if [[ "${OPS_MANAGER_REGISTRY}" == quay.io* ]]; then
      OPS_MANAGER_NAME=mongodb-enterprise-ops-manager-ubi
    fi
    if [[ "${APPDB_REGISTRY}" == quay.io* ]]; then
      APPDB_NAME=mongodb-enterprise-appdb-ubi
    fi
    if [[ "${DATABASE_REGISTRY}" == quay.io* ]]; then
      DATABASE_NAME=mongodb-enterprise-database-ubi
    fi
fi

deploy_test_app() {
    title "Deploying test application"

    helm_template_file=$(mktemp)
    BUNDLED_APP_DB_VERSION="$(jq --raw-output .appDbBundle.mongodbVersion < release.json)"
    # apply the correct configuration of the running OM instance
    # note, that the 4 last parameters are used only for Mongodb resource testing - not for Ops Manager
    helm_params=(
        "--set" "taskId=${task_id:-'not-specified'}"
        "--set" "repo=${TEST_APP_REGISTRY:=268558157000.dkr.ecr.us-east-1.amazonaws.com/dev}"
        "--set" "namespace=${PROJECT_NAMESPACE}"
        "--set" "taskName=${task_name}"
        "--set" "pytest.addopts=${pytest_addopts:-}"
        "--set" "operator.name=${OPERATOR_NAME:-mongodb-enterprise-operator}"
        "--set" "managedSecurityContext=${MANAGED_SECURITY_CONTEXT:-false}"
        "--set" "tag=${version_id:-$latest}"
        "--set" "aws.accessKey=${AWS_ACCESS_KEY_ID-}"
        "--set" "aws.secretAccessKey=${AWS_SECRET_ACCESS_KEY:-}"
        "--set" "skipExecution=${SKIP_EXECUTION:-'false'}"
        "--set" "baseUrl=${OM_BASE_URL:-http://ops-manager.${OPS_MANAGER_NAMESPACE}.svc.cluster.local:8080}"
        "--set" "apiKey=${OM_API_KEY:-}"
        "--set" "apiUser=${OM_USER:-admin}"
        "--set" "bundledAppDbVersion=${BUNDLED_APP_DB_VERSION}"
        "--set" "orgId=${OM_ORGID:-}"
        "--set" "operator.version=${version_id:-$latest}"
        "--set" "registry.operator=${REGISTRY}"
        "--set" "registry.initOpsManager=${INIT_OPS_MANAGER_REGISTRY}"
        "--set" "registry.initAppDb=${INIT_APPDB_REGISTRY}"
        "--set" "registry.initDatabase=${INIT_DATABASE_REGISTRY}"
        "--set" "registry.opsManager=${OPS_MANAGER_REGISTRY}"
        "--set" "registry.appDb=${APPDB_REGISTRY}"
        "--set" "registry.database=${DATABASE_REGISTRY}"
        "--set" "opsManager.name=${OPS_MANAGER_NAME:=mongodb-enterprise-ops-manager}"
        "--set" "appDb.name=${APPDB_NAME:=mongodb-enterprise-appdb}"
        "--set" "database.name=${DATABASE_NAME:=mongodb-enterprise-database}"
    )
    if [[ -n "${ecr_registry_needs_auth:-}" ]]; then
        echo "Configuring imagePullSecrets to ${ecr_registry_needs_auth}"
        helm_params+=("--set" "imagePullSecrets=${ecr_registry_needs_auth}")
    fi
    if [[ -n "${custom_om_version:-}" ]]; then
        # The test needs to create an OM resource with specific version
        helm_params+=("--set" "customOmVersion=${custom_om_version}")
    fi
    if [[ -n "${custom_mdb_version:-}" ]]; then
        # The test needs to test MongoDB of a specific version
        helm_params+=("--set" "customOmMdbVersion=${custom_mdb_version}")
    fi
    if [[ -n "${custom_mdb_prev_version:-}" ]]; then
        # The test needs to test MongoDB of a previous version
        helm_params+=("--set" "customOmMdbPrevVersion=${custom_mdb_prev_version}")
    fi


    helm template "scripts/evergreen/deployments/test-app" "${helm_params[@]}" > "${helm_template_file}" || exit 1

    kubectl -n "${PROJECT_NAMESPACE}" delete -f "${helm_template_file}" 2>/dev/null  || true

    kubectl -n "${PROJECT_NAMESPACE}" apply -f "${helm_template_file}"

    rm "${helm_template_file}"
}

wait_until_pod_is_running_or_failed_or_succeeded() {
    # Do wait while the Pod is not yet running or failed (can be in Pending or ContainerCreating state)
    # Note that the pod may jump to Failed/Completed state quickly - so we need to give up waiting on this as well
    echo "Waiting until the test application gets to Running state..."

    is_running_cmd="kubectl -n ${PROJECT_NAMESPACE} get pod ${TEST_APP_PODNAME} -o jsonpath={.status.phase} | grep -q 'Running'"

    # test app usually starts instantly but sometimes (quite rarely though) may require more than a min to start
    # in Evergreen so let's wait for 2m
    timeout --foreground "2m" bash -c "while ! ${is_running_cmd}; do printf .; sleep 1; done;"
    echo

    if ! eval "${is_running_cmd}"; then
        error "Test application failed to start on time!"
        kubectl -n "${PROJECT_NAMESPACE}"  describe pod "${TEST_APP_PODNAME}"
        fatal "Failed to run test application - exiting"
    fi
}

test_app_ended() {
    local status
    status="$(kubectl -n "${PROJECT_NAMESPACE}" get pod "${TEST_APP_PODNAME}" -o jsonpath="{.status.phase}")"
    [[ "${status}" = "Failed" || "${status}" = "Succeeded" ]]
}

# Will run the test application and wait for its completion.
run_tests() {
    local task_name=${1}

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
        kubectl -n "${PROJECT_NAMESPACE}" logs "${TEST_APP_PODNAME}" -f | tee "${output_filename}" || true
        kubectl -n "${PROJECT_NAMESPACE}" logs "deployment/mongodb-enterprise-operator" > "${operator_filename}"
    fi

    # Waiting a bit until the pod gets to some end
    while ! test_app_ended; do printf .; sleep 1; done;
    echo

    [[ $(kubectl -n "${PROJECT_NAMESPACE}" get pods/${TEST_APP_PODNAME} -o jsonpath='{.status.phase}') == "Succeeded" ]]
}

mkdir -p logs/

TESTS_OK=0
run_tests "${TEST_NAME}" || TESTS_OK=1

echo "Tests have finished with the following exit code: ${TESTS_OK}"

[[ "${TESTS_OK}" -eq 0 ]]
