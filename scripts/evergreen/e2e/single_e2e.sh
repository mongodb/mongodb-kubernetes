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
    meko_tests_version="${OPERATOR_VERSION}"

    local arch
    arch=$(uname -m)

    case "${arch}" in
        aarch64|arm64)
            meko_tests_version="${meko_tests_version}-arm64"
            ;;
        ppc64le)
            meko_tests_version="${meko_tests_version}-ppc64le"
            ;;
        s390x)
            meko_tests_version="${meko_tests_version}-s390x"
            ;;
        *)
            echo "amd64 host, using default meko_tests_version"
            ;;
    esac

    IS_PATCH="${IS_PATCH:-default_patch}"
    TASK_NAME="${TASK_NAME:-default_task}"
    EXECUTION="${EXECUTION:-default_execution}"
    BUILD_ID="${BUILD_ID:-default_build_id}"
    BUILD_VARIANT="${BUILD_VARIANT:-default_build_variant}"

    if ! chart_info=$(scripts/dev/run_python.sh scripts/release/oci_chart_info.py --build-scenario "${BUILD_SCENARIO}" 2>&1); then
        echo "${chart_info}"
        exit 1
    fi
    helm_oci_registry=$(echo "${chart_info}" | jq -r '.registry' )
    helm_oci_repository=$(echo "${chart_info}" | jq -r '.repository' )
    helm_oci_registry_region=$(echo "${chart_info}" | jq -r '.region' )
    helm_oci_version_prefix=$(echo "${chart_info}" | jq -r '.version_prefix // empty' )
    helm_oci_version="${helm_oci_version_prefix:-}${OPERATOR_VERSION}"

    # note, that the 4 last parameters are used only for Mongodb resource testing - not for Ops Manager
    helm_params=(
        "--set" "taskId=${task_id:-'not-specified'}"
        "--set" "namespace=${NAMESPACE}"
        "--set" "taskName=${task_name}"
        "--set" "mekoTestsRegistry=${MEKO_TESTS_REGISTRY}"
        "--set" "mekoTestsVersion=${meko_tests_version}"
        "--set" "versionId=${VERSION_ID}"
        "--set" "aws.accessKey=${AWS_ACCESS_KEY_ID}"
        "--set" "aws.secretAccessKey=${AWS_SECRET_ACCESS_KEY}"
        "--set" "skipExecution=${SKIP_EXECUTION:-'false'}"
        "--set" "baseUrl=${OM_BASE_URL:-http://ops-manager-svc.${OPS_MANAGER_NAMESPACE}.svc.cluster.local:8080}"
        "--set" "apiKey=${OM_API_KEY:-}"
        "--set" "apiUser=${OM_USER:-admin}"
        "--set" "orgId=${OM_ORGID:-}"
        "--set" "imagePullSecrets=image-registries-secret"
        "--set" "managedSecurityContext=${MANAGED_SECURITY_CONTEXT:-false}"
        "--set" "registry=${REGISTRY}"
        "--set" "mdbDefaultArchitecture=${MDB_DEFAULT_ARCHITECTURE:-'non-static'}"
        "--set" "mdbImageType=${MDB_IMAGE_TYPE:-'ubi8'}"
        "--set" "clusterDomain=${CLUSTER_DOMAIN:-'cluster.local'}"
        "--set" "cognito_user_pool_id=${cognito_user_pool_id}"
        "--set" "cognito_workload_federation_client_id=${cognito_workload_federation_client_id}"
        "--set" "cognito_user_name=${cognito_user_name}"
        "--set" "cognito_workload_federation_client_secret=${cognito_workload_federation_client_secret}"
        "--set" "cognito_user_password=${cognito_user_password}"
        "--set" "cognito_workload_url=${cognito_workload_url}"
        "--set" "cognito_workload_user_id=${cognito_workload_user_id}"
        "--set" "helm.oci.version=${helm_oci_version}"
        "--set" "helm.oci.registry=${helm_oci_registry}"
        "--set" "helm.oci.repository=${helm_oci_repository}"
        "--set" "helm.oci.region=${helm_oci_registry_region}"
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
    if [[ -n "${pytest_addopts:-}" ]]; then
        # The test needs to create an OM resource with specific version
        helm_params+=("--set" "pytest.addopts=${pytest_addopts:-}")
    fi
    # As soon as we are having one OTEL expansion it means we want to trace and send everything to our trace provider.
    # otel_parent_id is a special case (hence lower cased) since it is directly coming from evergreen and not via our
    # make switch mechanism. We need the "freshest" parent_id otherwise we are attaching to the wrong parent span.
    if [[ -n "${otel_parent_id:-}" ]]; then
        otel_resource_attributes="evergreen.version.id=${VERSION_ID:-},evergreen.version.requester=${requester:-},mck.git_branch=${branch_name:-},evergreen.version.pr_num=${github_pr_number:-},mck.git_commit=${github_commit:-},mck.revision=${revision:-},is_patch=${IS_PATCH},evergreen.task.name=${TASK_NAME},evergreen.task.execution=${EXECUTION},evergreen.build.id=${BUILD_ID},evergreen.build.name=${BUILD_VARIANT},evergreen.task.id=${task_id},evergreen.project.id=${project_identifier:-}"
        # shellcheck disable=SC2001
        escaped_otel_resource_attributes=$(echo "${otel_resource_attributes}" | sed 's/,/\\,/g')
        # The test needs to create an OM resource with specific version
        helm_params+=("--set" "otel_parent_id=${otel_parent_id:-"unknown"}")
        helm_params+=("--set" "otel_trace_id=${OTEL_TRACE_ID:-"unknown"}")
        helm_params+=("--set" "otel_endpoint=${OTEL_COLLECTOR_ENDPOINT:-"unknown"}")
        helm_params+=("--set" "otel_resource_attributes=${escaped_otel_resource_attributes}")
    fi
    if [[ -n "${CUSTOM_OM_PREV_VERSION:-}" ]]; then
        # The test needs to create an OM resource with specific version
        helm_params+=("--set" "customOmPrevVersion=${CUSTOM_OM_PREV_VERSION}")
    fi
    if [[ -n "${PERF_TASK_DEPLOYMENTS:-}" ]]; then
        # The test needs to create an OM resource with specific version
        helm_params+=("--set" "taskDeployments=${PERF_TASK_DEPLOYMENTS}")
    fi
    if [[ -n "${PERF_TASK_REPLICAS:-}" ]]; then
        # The test needs to create an OM resource with specific version
        helm_params+=("--set" "taskReplicas=${PERF_TASK_REPLICAS}")
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

    if [[ -n "${PROJECT_DIR:-}" ]]; then
        helm_params+=("--set" "projectDir=/mongodb-kubernetes")
    fi

    if [[ "${LOCAL_OPERATOR}" == true ]]; then
        helm_params+=("--set" "localOperator=true")
    fi

    if [[ "${OM_DEBUG_HTTP}" == "true" ]]; then
        helm_params+=("--set" "omDebugHttp=true")
    fi

    helm_params+=("--set" "opsManagerVersion=${ops_manager_version}")

    helm template "scripts/evergreen/deployments/test-app" "${helm_params[@]}" > "${helm_template_file}" || exit 1

    cat "${helm_template_file}"

    kubectl --context "${context}" -n "${NAMESPACE}" delete -f "${helm_template_file}" 2>/dev/null  || true

    kubectl --context "${context}" -n "${NAMESPACE}" apply -f "${helm_template_file}"
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

    test_pod_context="${operator_context}"
    if [[ "${KUBE_ENVIRONMENT_NAME}" = "multi" ]]; then
        operator_context="${CENTRAL_CLUSTER}"
        # shellcheck disable=SC2154,SC2269
        test_pod_context="${test_pod_cluster:-${operator_context}}"
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

        # At this time ${TEST_APP_PODNAME} has finished running, so we don't follow (-f) it
        # Similarly, the operator deployment has finished with our tests, so we print whatever we have
        # until this moment and go continue with our lives
        kubectl --context "${test_pod_context}" -n "${NAMESPACE}" logs "${TEST_APP_PODNAME}" -c mongodb-enterprise-operator-tests -f | tee "${output_filename}" || true
    fi


    # Waiting a bit until the pod gets to some end
    while ! test_app_ended "${test_pod_context}"; do printf .; sleep 1; done;
    echo

    # We need to make sure to access this file after the test has finished
    kubectl --context "${test_pod_context}" -n "${NAMESPACE}" -c keepalive cp "${TEST_APP_PODNAME}":/tmp/results/myreport.xml logs/myreport.xml
    kubectl --context "${test_pod_context}" -n "${NAMESPACE}" -c keepalive cp "${TEST_APP_PODNAME}":/tmp/diagnostics logs

    status="$(kubectl --context "${test_pod_context}" get pod "${TEST_APP_PODNAME}" -n "${NAMESPACE}" -o jsonpath="{ .status }" | jq -r '.containerStatuses[] | select(.name == "mongodb-enterprise-operator-tests")'.state.terminated.reason)"
    [[ "${status}" == "Completed" ]]
}

mkdir -p logs/

TESTS_OK=0
# shellcheck disable=SC2153
run_tests "${TEST_NAME}" || TESTS_OK=1

echo "Tests have finished with the following exit code: ${TESTS_OK}"

[[ "${TESTS_OK}" -eq 0 ]]
