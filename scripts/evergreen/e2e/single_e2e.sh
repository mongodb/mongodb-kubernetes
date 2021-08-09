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
    # shellcheck disable=SC2153
    if [[ "${DATABASE_REGISTRY}" == quay.io* ]]; then
      DATABASE_NAME=mongodb-enterprise-database-ubi
    fi
fi

prepare_operator_config_map() {
    local context=${1}
    title "Preparing the ConfigMap with Operator installation configuration"

    kubectl --context "${context}" delete configmap operator-installation-config --ignore-not-found
    local version_id=${version_id:=latest}

    local operator_version="${version_id}"
    local database_registry=${DATABASE_REGISTRY}
    local database_name=${DATABASE_NAME}
    if [[ ${IMAGE_TYPE} == "usaf" ]]; then
      # shellcheck disable=SC2154
      operator_version="${usaf_operator_version}"
      # in 1.7.1, the MONGODB_ENTERPRISE_DATABASE_IMAGE is the registry and tag
      # and is created from ${DATABASE_REGISTRY}/${DATABASE_NAME}
      # shellcheck disable=SC2154
      database_registry="${ecr_registry}/dev/usaf"
      # shellcheck disable=SC2154
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
      "--from-literal" "database.name=${database_name:=mongodb-enterprise-database}"
      "--from-literal" "operator.version=${operator_version}"
      "--from-literal" "initOpsManager.version=${version_id}"
      "--from-literal" "initAppDb.version=${version_id}"
      "--from-literal" "initDatabase.version=${version_id}"
      "--from-literal" "agent.version=${agent_version}"
    )

    if [[ ${IMAGE_TYPE} == "ubi" ]]; then
      config+=("--from-literal" "agent.name=mongodb-agent-ubi")
      config+=("--from-literal" "mongodb.name=mongodb-enterprise-appdb-database-ubi")
    else
      config+=("--from-literal" "agent.name=mongodb-agent")
      config+=("--from-literal" "mongodb.name=mongodb-enterprise-appdb-database")
    fi

     # shellcheck disable=SC2154
    if [[ "${kube_environment_name}" = "multi" ]]; then
       # shellcheck disable=SC2154
       comma_separated_list="$(echo "${member_clusters}" | tr ' ' ',')"
       config+=("--from-literal" "multiCluster.clusters={${comma_separated_list}}")
    fi

    if [[ "${USE_RUNNING_OPERATOR:-}" == "true" ]]; then
      config+=("--from-literal useRunningOperator=true")
    fi

    # shellcheck disable=SC2086
    kubectl --context "${context}" create configmap operator-installation-config -n "${PROJECT_NAMESPACE}" ${config[*]}
    # for some reasons the previous 'create' command doesn't return >0 in case of failures...
    ! kubectl --context "${context}"  get configmap operator-installation-config -n "${PROJECT_NAMESPACE}" && \
          fatal "Failed to create ConfigMap operator-installation-config"

}

ensure_test_namespace(){
    local context=${1}
    kubectl create ns --context "${context}"  "${PROJECT_NAMESPACE}"  || true
    kubectl label ns "${PROJECT_NAMESPACE}" --context "${context}" "evg=task" || true
    # shellcheck disable=SC2154
    kubectl annotate ns "${PROJECT_NAMESPACE}" --context "${context}" "evg/task=https://evergreen.mongodb.com/task/${task_id}"
}

configure_multi_cluster_environment(){
    echo "Running a multi cluster test, configuring e2e roles in all clusters and kubeconfig secret."

    echo "Ensuring namespaces"
    # shellcheck disable=SC2154
    ensure_test_namespace "${central_cluster}"
    for member_cluster in ${member_clusters}; do
      ensure_test_namespace "${member_cluster}"
      kubectl --context "${member_cluster}" label ns "${PROJECT_NAMESPACE}" istio-injection=enabled
    done


    helm_template_file=$(mktemp)

    helm_params=(
        "--set" "namespace=${PROJECT_NAMESPACE}"
        "--set" "imagePullSecrets=image-registries-secret"
    )

    helm template "scripts/evergreen/deployments/multi-cluster-roles" "${helm_params[@]}" > "${helm_template_file}" || exit 1


    echo "Creating KubeConfig secret for test pod in namespace ${PROJECT_NAMESPACE}}"
    kubectl --context "${central_cluster}" create secret generic test-pod-kubeconfig --from-file=kubeconfig="${KUBECONFIG}" --namespace "${PROJECT_NAMESPACE}"

    echo "Creating project configmap"
    # delete `my-project` if it exists
    kubectl --context "${central_cluster}" --namespace "${PROJECT_NAMESPACE}" delete configmap my-project --ignore-not-found
    # Configuring project
    kubectl --context "${central_cluster}" --namespace "${PROJECT_NAMESPACE}" create configmap my-project \
            --from-literal=projectName="${PROJECT_NAMESPACE}" --from-literal=baseUrl="${OM_BASE_URL}" \
            --from-literal=orgId="${OM_ORGID:-}"

    echo "Creating credentials secret"
    # delete `my-credentials` if it exists
    kubectl --context "${central_cluster}" --namespace "${PROJECT_NAMESPACE}" delete  secret my-credentials  --ignore-not-found
    # Configure the Kubernetes credentials for Ops Manager
    kubectl --context "${central_cluster}" --namespace "${PROJECT_NAMESPACE}" create secret generic my-credentials \
            --from-literal=user="${OM_USER:=admin}" --from-literal=publicApiKey="${OM_API_KEY}"

    echo "Creating required roles and service accounts."
    kubectl --context "${central_cluster}" -n "${PROJECT_NAMESPACE}" apply -f "${helm_template_file}"
    for member_cluster in ${member_clusters}; do
      kubectl --context "${member_cluster}" -n "${PROJECT_NAMESPACE}" apply -f "${helm_template_file}"
    done

    rm "${helm_template_file}"

    # wait some time for service account token secrets to appear.
    sleep 3

    local service_account_name="operator-tests-multi-cluster-service-account"

    local secret_name
    secret_name="$(kubectl --context  "${central_cluster}" get secret -n "${PROJECT_NAMESPACE}" | grep "${service_account_name}"  | awk '{ print $1 }')"

    local central_cluster_token
    central_cluster_token="$(kubectl --context "${central_cluster}" get secret "${secret_name}" -o jsonpath='{ .data.token}' -n "${PROJECT_NAMESPACE}" | base64 -d)"
    echo "Creating Multi Cluster configuration secret"

    configuration_params=(
      "--from-literal=${central_cluster}=${central_cluster_token}"
      "--from-literal=central_cluster=${central_cluster}"
    )

    INDEX=1
    for member_cluster in ${member_clusters}; do
      secret_name="$(kubectl --context  "${member_cluster}" get secret -n "${PROJECT_NAMESPACE}" | grep "${service_account_name}"  | awk '{ print $1 }')"
      member_cluster_token="$(kubectl --context "${member_cluster}" get secret "${secret_name}" -o jsonpath='{ .data.token}' -n "${PROJECT_NAMESPACE}" | base64 -d)"
      configuration_params+=(
         "--from-literal=${member_cluster}=${member_cluster_token}"
         "--from-literal=member_cluster_${INDEX}=${member_cluster}"
      )
      (( INDEX++ ))
    done

    kubectl --context "${central_cluster}"  create secret generic test-pod-multi-cluster-config -n "${PROJECT_NAMESPACE}" "${configuration_params[@]}"
}

deploy_test_app() {
    title "Deploying test application"
    local context=${1}
    local helm_template_file
    helm_template_file=$(mktemp)
    # apply the correct configuration of the running OM instance
    # note, that the 4 last parameters are used only for Mongodb resource testing - not for Ops Manager
    helm_params=(
        "--set" "taskId=${task_id:-'not-specified'}"
        "--set" "repo=${TEST_APP_REGISTRY:=268558157000.dkr.ecr.us-east-1.amazonaws.com/dev}"
        "--set" "namespace=${PROJECT_NAMESPACE}"
        "--set" "taskName=${task_name}"
        "--set" "pytest.addopts=${pytest_addopts:-}"
        "--set" "tag=${version_id:-$latest}"
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
    )

    # shellcheck disable=SC2154
    if [[ ${kube_environment_name} = "multi" ]]; then
        helm_params+=("--set" "multiCluster.memberClusters=${member_clusters}")
        helm_params+=("--set" "multiCluster.centralCluster=${central_cluster}")
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
    if [[ -n "${custom_appdb_version:-}" ]]; then
        helm_params+=("--set" "customAppDbVersion=${custom_appdb_version}")
    fi

    if [[ -n "${GITHUB_TOKEN_READ:-}" ]]; then
        helm_params+=("--set" "githubToken=${GITHUB_TOKEN_READ}")
    fi

    helm template "scripts/evergreen/deployments/test-app" "${helm_params[@]}" > "${helm_template_file}" || exit 1

    kubectl --context "${context}" -n "${PROJECT_NAMESPACE}" delete -f "${helm_template_file}" 2>/dev/null  || true

    kubectl --context "${context}" -n "${PROJECT_NAMESPACE}" apply -f "${helm_template_file}"

    rm "${helm_template_file}"
}

wait_until_pod_is_running_or_failed_or_succeeded() {
    local context=${1}
    # Do wait while the Pod is not yet running or failed (can be in Pending or ContainerCreating state)
    # Note that the pod may jump to Failed/Completed state quickly - so we need to give up waiting on this as well
    echo "Waiting until the test application gets to Running state..."

    is_running_cmd="kubectl --context ${context} -n ${PROJECT_NAMESPACE} get pod ${TEST_APP_PODNAME} -o jsonpath={.status.phase} | grep -q 'Running'"

    # test app usually starts instantly but sometimes (quite rarely though) may require more than a min to start
    # in Evergreen so let's wait for 2m
    timeout --foreground "2m" bash -c "while ! ${is_running_cmd}; do printf .; sleep 1; done;"
    echo

    if ! eval "${is_running_cmd}"; then
        error "Test application failed to start on time!"
        kubectl --context "${context}" -n "${PROJECT_NAMESPACE}"  describe pod "${TEST_APP_PODNAME}"
        fatal "Failed to run test application - exiting"
    fi
}

test_app_ended() {
    local context="${1}"
    local status
    status="$(kubectl --context "${context}" -n "${PROJECT_NAMESPACE}" get pod "${TEST_APP_PODNAME}" -o jsonpath="{.status.phase}")"
    [[ "${status}" = "Failed" || "${status}" = "Succeeded" ]]
}

# Will run the test application and wait for its completion.
run_tests() {
    local task_name=${1}
    local context
    context="$(kubectl config current-context)"
    if [[ "${kube_environment_name}" = "multi" ]]; then
        context="${central_cluster}"
    fi

    TEST_APP_PODNAME=mongodb-enterprise-operator-tests

    if [[ "${kube_environment_name}" = "multi" ]]; then
      configure_multi_cluster_environment
    fi

    prepare_operator_config_map "${context}"

    deploy_test_app "${context}"

    wait_until_pod_is_running_or_failed_or_succeeded "${context}"

    title "Running e2e test ${task_name} (tag: ${TEST_IMAGE_TAG})"

    # we don't output logs to file when running tests locally
    if [[ "${MODE-}" == "dev" ]]; then
        kubectl -n "${PROJECT_NAMESPACE}" logs "${TEST_APP_PODNAME}" -f
    else
        output_filename="logs/test_app.log"
        operator_filename="logs/0_operator.log"

        # At this time both ${TEST_APP_PODNAME} have finished running, so we don't follow (-f) it
        # Similarly, the operator deployment has finished with our tests, so we print whatever we have
        # until this moment and go continue with our lives
        kubectl --context "${context}" -n "${PROJECT_NAMESPACE}" logs "${TEST_APP_PODNAME}" -f | tee "${output_filename}" || true
        kubectl --context "${context}" -n "${PROJECT_NAMESPACE}" logs "deployment/mongodb-enterprise-operator" > "${operator_filename}"
    fi

    # Waiting a bit until the pod gets to some end
    while ! test_app_ended "${context}"; do printf .; sleep 1; done;
    echo

    [[ $(kubectl --context "${context}" -n "${PROJECT_NAMESPACE}" get pods/${TEST_APP_PODNAME} -o jsonpath='{.status.phase}') == "Succeeded" ]]
}

mkdir -p logs/

TESTS_OK=0
run_tests "${TEST_NAME}" || TESTS_OK=1

echo "Tests have finished with the following exit code: ${TESTS_OK}"

[[ "${TESTS_OK}" -eq 0 ]]
