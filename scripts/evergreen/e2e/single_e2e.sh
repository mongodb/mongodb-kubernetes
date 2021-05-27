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

prepare_operator_config_map() {
    title "Preparing the ConfigMap with Operator installation configuration"

    kubectl delete configmap operator-installation-config --ignore-not-found
    local version_id=${version_id:=latest}

    local operator_version="${version_id}"
    local database_registry=${DATABASE_REGISTRY}
    local database_name=${DATABASE_NAME}
    if [[ ${IMAGE_TYPE} == "usaf" ]]; then
      operator_version="${usaf_operator_version}"
      # in 1.7.1, the MONGODB_ENTERPRISE_DATABASE_IMAGE is the registry and tag
      # and is created from ${DATABASE_REGISTRY}/${DATABASE_NAME}
      database_registry="${ecr_registry}/dev/usaf"
      database_name="mongodb-enterprise-database:${usaf_database_version}"
    fi

    config=(
      "--from-literal" "managedSecurityContext=${MANAGED_SECURITY_CONTEXT:-false}"
      "--from-literal" "registry.operator=${REGISTRY}"
      "--from-literal" "registry.imagePullSecrets=image-registries-secret"
      "--from-literal" "registry.initOpsManager=${INIT_OPS_MANAGER_REGISTRY}"
      "--from-literal" "registry.initAppDb=${INIT_APPDB_REGISTRY}"
      "--from-literal" "registry.initDatabase=${INIT_DATABASE_REGISTRY}"
      "--from-literal" "registry.opsManager=${OPS_MANAGER_REGISTRY}"
      "--from-literal" "registry.appDb=${APPDB_REGISTRY}"
      "--from-literal" "registry.database=${database_registry}"
      "--from-literal" "opsManager.name=${OPS_MANAGER_NAME:=mongodb-enterprise-ops-manager}"
      "--from-literal" "appDb.name=${APPDB_NAME:=mongodb-enterprise-appdb}"
      "--from-literal" "database.name=${database_name:=mongodb-enterprise-database}"
      "--from-literal" "operator.version=${operator_version}"
      "--from-literal" "initOpsManager.version=${version_id}"
      "--from-literal" "initAppDb.version=${version_id}"
      "--from-literal" "initDatabase.version=${version_id}"
    )

    if [[ "${USE_RUNNING_OPERATOR:-}" == "true" ]]; then
      config+=("--from-literal useRunningOperator=true")
    fi

    kubectl create configmap operator-installation-config -n "${PROJECT_NAMESPACE}" ${config[*]}
    # for some reasons the previous 'create' command doesn't return >0 in case of failures...
    ! kubectl get configmap operator-installation-config -n "${PROJECT_NAMESPACE}" && \
          fatal "Failed to create ConfigMap operator-installation-config"

}

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
        "--set" "tag=${version_id:-$latest}"
        "--set" "aws.accessKey=${AWS_ACCESS_KEY_ID-}"
        "--set" "aws.secretAccessKey=${AWS_SECRET_ACCESS_KEY:-}"
        "--set" "skipExecution=${SKIP_EXECUTION:-'false'}"
        "--set" "baseUrl=${OM_BASE_URL:-http://ops-manager.${OPS_MANAGER_NAMESPACE}.svc.cluster.local:8080}"
        "--set" "apiKey=${OM_API_KEY:-}"
        "--set" "apiUser=${OM_USER:-admin}"
        "--set" "bundledAppDbVersion=${BUNDLED_APP_DB_VERSION}"
        "--set" "orgId=${OM_ORGID:-}"
        "--set" "imageType=${IMAGE_TYPE}"
        "--set" "imagePullSecrets=image-registries-secret"

    )
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

    if [[ -n "${GITHUB_TOKEN_READ:-}" ]]; then
        helm_params+=("--set" "githubToken=${GITHUB_TOKEN_READ}")
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

    prepare_operator_config_map

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
