#!/usr/bin/env bash

set -Eeou pipefail

##
## The script deploys a single test application and waits until it finishes.
## All the Operator deployment, configuration and teardown work is done in 'e2e' script
##

source scripts/funcs/checks
source scripts/funcs/printing
source scripts/funcs/errors
source scripts/funcs/multicluster
source scripts/funcs/operator_deployment

check_env_var "TEST_NAME" "The 'TEST_NAME' must be specified to run the Operator single e2e test"


deploy_test_app() {
    printenv
    title "Deploying test application"
    local context=${1}
    local helm_template_file
    helm_template_file=$(mktemp)
    tag="${VERSION_ID:-latest}"
    if [[ "${OVERRIDE_VERSION_ID:-}" != "" ]]; then
      tag="${OVERRIDE_VERSION_ID}"
    fi
    # apply the correct configuration of the running OM instance
    # note, that the 4 last parameters are used only for Mongodb resource testing - not for Ops Manager
    helm_params=(
        "--set" "taskId=${task_id:-'not-specified'}"
        "--set" "repo=${TEST_APP_REGISTRY:=268558157000.dkr.ecr.us-east-1.amazonaws.com/dev}"
        "--set" "namespace=${NAMESPACE}"
        "--set" "taskName=${task_name}"
        "--set" "pytest.addopts=${pytest_addopts:-}"
        "--set" "tag=${tag}"
        "--set" "aws.accessKey=${AWS_ACCESS_KEY_ID}"
        "--set" "aws.secretAccessKey=${AWS_SECRET_ACCESS_KEY}"
        "--set" "skipExecution=${SKIP_EXECUTION:-'false'}"
        "--set" "baseUrl=${OM_BASE_URL:-http://ops-manager-svc.${OPS_MANAGER_NAMESPACE}.svc.cluster.local:8080}"
        "--set" "apiKey=${OM_API_KEY:-}"
        "--set" "apiUser=${OM_USER:-admin}"
        "--set" "orgId=${OM_ORGID:-}"
        "--set" "imageType=${IMAGE_TYPE}"
        "--set" "imagePullSecrets=image-registries-secret"
        "--set" "managedSecurityContext=${MANAGED_SECURITY_CONTEXT:-false}"
        "--set" "registry=${REGISTRY:-${BASE_REPO_URL}/${IMAGE_TYPE}}"
    )

    # shellcheck disable=SC2154
    if [[ ${KUBE_ENVIRONMENT_NAME} = "multi" ]]; then
        helm_params+=("--set" "multiCluster.memberClusters=${MEMBER_CLUSTERS}")
        helm_params+=("--set" "multiCluster.centralCluster=${CENTRAL_CLUSTER}")
        helm_params+=("--set" "multiCluster.testPodCluster=${test_pod_cluster}")
    fi

    if [[ -n "${CUSTOM_OM_VERSION:-}" ]]; then
        # The test needs to create an OM resource with specific version
        helm_params+=("--set" "customOmVersion=${CUSTOM_OM_VERSION}")
    fi
    if [[ -n "${CUSTOM_OM_PREV_VERSION:-}" ]]; then
        # The test needs to create an OM resource with specific version
        helm_params+=("--set" "customOmPrevVersion=${CUSTOM_OM_PREV_VERSION}")
    fi
    if [[ -n "${CUSTOM_MDB_VERSION:-}" ]]; then
        # The test needs to test MongoDB of a specific version
        helm_params+=("--set" "customOmMdbVersion=${CUSTOM_MDB_VERSION}")
    fi
    if [[ -n "${CUSTOM_MDB_PREV_VERSION:-}" ]]; then
        # The test needs to test MongoDB of a previous version
        helm_params+=("--set" "customOmMdbPrevVersion=${CUSTOM_MDB_PREV_VERSION}")
    fi
    if [[ -n "${CUSTOM_APPDB_VERSION:-}" ]]; then
        helm_params+=("--set" "customAppDbVersion=${CUSTOM_APPDB_VERSION}")
    fi

    if [[ -n "${GITHUB_TOKEN_READ:-}" ]]; then
        helm_params+=("--set" "githubToken=${GITHUB_TOKEN_READ}")
    fi

    if [[ "$LOCAL_OPERATOR" == true ]]; then
        helm_params+=("--set" "localOperator=true")
    fi

    helm template "scripts/evergreen/deployments/test-app" "${helm_params[@]}" > "${helm_template_file}" || exit 1

    cat "${helm_template_file}"

    kubectl --context "${context}" -n "${NAMESPACE}" delete -f "${helm_template_file}" 2>/dev/null  || true

    kubectl --context "${context}" -n "${NAMESPACE}" apply -f "${helm_template_file}"

    rm "${helm_template_file}"
}

wait_until_pod_is_running_or_failed_or_succeeded() {
    local context=${1}
    # Do wait while the Pod is not yet running or failed (can be in Pending or ContainerCreating state)
    # Note that the pod may jump to Failed/Completed state quickly - so we need to give up waiting on this as well
    echo "Waiting until the test application gets to Running state..."

    is_running_cmd="kubectl --context '${context}' -n ${NAMESPACE} get pod ${TEST_APP_PODNAME} -o jsonpath={.status.phase} | grep -q 'Running'"

    # test app usually starts instantly but sometimes (quite rarely though) may require more than a min to start
    # in Evergreen so let's wait for 2m
    timeout --foreground "2m" bash -c "while ! ${is_running_cmd}; do printf .; sleep 1; done;"
    echo

    if ! eval "${is_running_cmd}"; then
        error "Test application failed to start on time!"
        kubectl --context "${context}" -n "${NAMESPACE}"  describe pod "${TEST_APP_PODNAME}"
        fatal "Failed to run test application - exiting"
    fi
}

test_app_ended() {
    local context="${1}"
    local status
    status="$(kubectl --context "${context}" get pod mongodb-enterprise-operator-tests -n "${NAMESPACE}" -o jsonpath="{.status}" | jq -r '.containerStatuses[] | select(.name == "mongodb-enterprise-operator-tests")'.state.terminated.reason)"
    [[ "${status}" = "Error" || "${status}" = "Completed" ]]
}

# Will run the test application and wait for its completion.
run_tests() {
    local task_name=${1}
    local operator_context
    local test_pod_context
    operator_context="$(kubectl config current-context)"
    operator_container_name="mongodb-enterprise-operator"

    test_pod_context="${operator_context}"
    if [[ "${KUBE_ENVIRONMENT_NAME}" = "multi" ]]; then
        operator_context="${CENTRAL_CLUSTER}"
         operator_container_name="mongodb-enterprise-operator-multi-cluster"
        # shellcheck disable=SC2154,SC2269
        test_pod_context="${test_pod_cluster:-$operator_context}"
    fi

    echo "Operator running in cluster ${operator_context}"
    echo "Test pod running in cluster ${test_pod_context}"

    TEST_APP_PODNAME=mongodb-enterprise-operator-tests

    if [[ "${KUBE_ENVIRONMENT_NAME}" = "multi" ]]; then
        configure_multi_cluster_environment
    fi

    prepare_operator_config_map "${operator_context}"

    deploy_test_app "${test_pod_context}"

    wait_until_pod_is_running_or_failed_or_succeeded "${test_pod_context}"

    title "Running e2e test ${task_name} (tag: ${TEST_IMAGE_TAG})"

    # we don't output logs to file when running tests locally
    if [[ "${MODE-}" == "dev" ]]; then
        kubectl --context "${test_pod_context}" -n "${NAMESPACE}" logs "${TEST_APP_PODNAME}" -c mongodb-enterprise-operator-tests -f
    else
        output_filename="logs/test_app.log"
        operator_filename="logs/0_operator.log"

        # At this time ${TEST_APP_PODNAME} has finished running, so we don't follow (-f) it
        # Similarly, the operator deployment has finished with our tests, so we print whatever we have
        # until this moment and go continue with our lives
        kubectl --context "${test_pod_context}" -n "${NAMESPACE}" logs "${TEST_APP_PODNAME}" -c mongodb-enterprise-operator-tests -f | tee "${output_filename}" || true
        kubectl --context "${operator_context}" -n "${NAMESPACE}" logs -l app.kubernetes.io/component=controller -c "${operator_container_name}" --tail -1 > "${operator_filename}"

    fi


    # Waiting a bit until the pod gets to some end
    while ! test_app_ended "${test_pod_context}"; do printf .; sleep 1; done;
    echo

    # We need to make sure to access this file after the test has finished
    kubectl --context "${test_pod_context}" -n "${NAMESPACE}" cp "${TEST_APP_PODNAME}":/tmp/results/myreport.xml logs/myreport.xml

    status="$(kubectl --context "${test_pod_context}" get pod "${TEST_APP_PODNAME}" -n "${NAMESPACE}" -o jsonpath="{ .status }" | jq -r '.containerStatuses[] | select(.name == "mongodb-enterprise-operator-tests")'.state.terminated.reason)"
    [[ "${status}" == "Completed" ]]
}

mkdir -p logs/

TESTS_OK=0
run_tests "${TEST_NAME}" || TESTS_OK=1

echo "Tests have finished with the following exit code: ${TESTS_OK}"

[[ "${TESTS_OK}" -eq 0 ]]
