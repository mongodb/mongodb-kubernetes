#!/usr/bin/env bash

set -euo pipefail

##
## Setups environment and runs e2e test
##

cd "$(git rev-parse --show-toplevel || echo "Failed to find git root"; exit 1)"
source scripts/funcs


if [ -n "${STATIC_NAMESPACE-}" ]; then
    PROJECT_NAMESPACE=${STATIC_NAMESPACE}
elif [ -z "${PROJECT_NAMESPACE-}" ]; then
    PROJECT_NAMESPACE=$(generate_random_namespace)
    export PROJECT_NAMESPACE
fi

if [[ ! -z "${STATIC_NAMESPACE-}" ]]; then
    echo "Waiting for static namespace ${STATIC_NAMESPACE} to be deleted before use"
    wait_for_namespace_to_be_deleted "${STATIC_NAMESPACE}"
    echo "Namespace ${STATIC_NAMESPACE} is available for use"
fi

if [[ "${BUILD_VARIANT-}" = "e2e_openshift_origin_v3.11_ops_manager" ]]; then
    # we need to allow running containers as ROOT for ops manager tests in Openshift
    oc adm policy add-scc-to-user anyuid system:serviceaccount:${PROJECT_NAMESPACE}:default
fi

ensure_namespace "${PROJECT_NAMESPACE}"

# Array contains string; based on https://stackoverflow.com/questions/3685970/check-if-a-bash-array-contains-a-value
contains() {
    local e match=$1
    shift
    for e; do [[ "$e" == "$match" ]] && return 0; done
    return 1
}

fetch_om_information() {
    if [[ "${TEST_MODE:-}" = "opsmanager" ]]; then
        echo "Skipping Ops Manager connection configuration as current test is for Ops Manager"
        return
    fi

    title "Reading Ops Manager environment variables..."

    if [ -z "$OPS_MANAGER_NAMESPACE" ]; then
        echo "OPS_MANAGER_NAMESPACE must be set!"
        exit 1
    fi

    OPERATOR_TESTING_FRAMEWORK_NS=${OPS_MANAGER_NAMESPACE}
    if ! kubectl get "namespace/${OPERATOR_TESTING_FRAMEWORK_NS}" &> /dev/null; then
        error "Ops Manager is not installed in this cluster. Make sure the Ops Manager installation script is called beforehand. Exiting..."

        exit 1
    else
        echo "Ops Manager is already installed in this cluster. Will reuse it now."
    fi

    # Get the environment from the ops-manager container
    echo "Getting credentials from Ops Manager"

    # Gets the om-environment
    kubectl -n "${OPERATOR_TESTING_FRAMEWORK_NS}" exec mongodb-enterprise-ops-manager-0 ls "/opt/mongodb/mms/env/.ops-manager-env" && \
        eval "$(kubectl -n "${OPERATOR_TESTING_FRAMEWORK_NS}" exec mongodb-enterprise-ops-manager-0 cat "/opt/mongodb/mms/env/.ops-manager-env")" || exit 1

    echo "OM_USER: ${OM_USER}"
    echo "OM_PASSWORD: ${OM_PASSWORD}"
    echo "OM_API_KEY: ${OM_API_KEY}"

    title "Ops Manager environment is successfully read"
}

configure_operator() {
    if [[ "${TEST_MODE:-}" = "opsmanager" ]]; then
        echo "Creating admin secret for the new Ops Manager instance"
        kubectl create secret generic ops-manager-admin-secret  \
            --from-literal=Username="jane.doe@example.com" \
            --from-literal=Password="Passw0rd." \
            --from-literal=FirstName="Jane" \
            --from-literal=LastName="Doe" -n ${PROJECT_NAMESPACE}

        echo "Admin secret created"
        return
    fi

    title "Creating project and credentials Kubernetes object..."
    if [[ "${OPERATOR_UPGRADE_IN_PROGRESS-}" = "stage2" ]]; then
        echo "Upgrade in progress, skipping configuration of projects"
        return
    fi

    if [ -n "${OM_BASE_URL-}" ]; then
      BASE_URL="${OM_BASE_URL}"
    else
      BASE_URL="http://ops-manager.${OPS_MANAGER_NAMESPACE:-}.svc.cluster.local:8080"
    fi

    # delete `my-project` if it exists
    ! kubectl --namespace "${PROJECT_NAMESPACE}" get configmaps | grep -q my-project \
        || kubectl --namespace "${PROJECT_NAMESPACE}" delete configmap my-project
    # Configuring project
    kubectl --namespace "${PROJECT_NAMESPACE}" create configmap my-project \
            --from-literal=projectName="${PROJECT_NAMESPACE}" --from-literal=baseUrl="${BASE_URL}" \
            --from-literal=credentials="my-credentials" \
            --from-literal=orgId="${OM_ORGID:-}"

    # delete `my-credentials` if it exists
    ! kubectl --namespace "${PROJECT_NAMESPACE}" get secrets | grep -q my-credentials \
        || kubectl --namespace "${PROJECT_NAMESPACE}" delete secret my-credentials
    # Configure the Kubernetes credentials for Ops Manager
    kubectl --namespace "${PROJECT_NAMESPACE}" create secret generic my-credentials \
            --from-literal=user="${OM_USER:=admin}" --from-literal=publicApiKey="${OM_API_KEY}"


    title "Credentials, Project have been created"
}

teardown() {
    kubectl delete mdb --all -n "${PROJECT_NAMESPACE}" || true
    kubectl delete mdbu --all -n "${PROJECT_NAMESPACE}" || true
    echo "Removing test namespace"
    kubectl delete "namespace/${PROJECT_NAMESPACE}" --wait=false || true
}

deploy_test_app() {
    title "Deploying test application"

    GIT_SHA=$(git rev-parse HEAD)

    # If running in evergreen, prefer the VERSION_ID to avoid GIT_SHA collisions
    # when people is building images from same commit.

    # Set the test_image_tag outside this function
    # TEST_IMAGE_TAG="${VERSION_ID:-$GIT_SHA}"
    if [ -z "${TEST_IMAGE_TAG-}" ]; then
        TEST_IMAGE_TAG="${VERSION_ID:-$GIT_SHA}"
    fi

    charttmpdir=$(mktemp -d 2>/dev/null || mktemp -d -t 'charttmpdir')
    charttmpdir=${charttmpdir}/chart
    cp -R "public/helm_chart/" "${charttmpdir}"

    cp scripts/evergreen/deployments/mongodb-enterprise-tests.yaml "${charttmpdir}/templates"

    pytest_addopts=""
    if [[ "${MODE-}" == "dev" ]]; then
        pytest_addopts="-s"
    fi

    # apply the correct configuration of the running OM instance
    # note, that the 4 last parameters are used only for Mongodb resource testing - not for Ops Manager
    helm template "${charttmpdir}" \
         -x templates/mongodb-enterprise-tests.yaml \
         --set repo="${REPO_URL:=268558157000.dkr.ecr.us-east-1.amazonaws.com/dev}" \
         --set namespace="${PROJECT_NAMESPACE}" \
         --set taskName="${task_name}" \
         --set pytest.addopts="${pytest_addopts}" \
         --set operator.name="${OPERATOR_NAME:=mongodb-enterprise-operator}" \
         --set managedSecurityContext="${MANAGED_SECURITY_CONTEXT:=false}" \
         --set tag="${TEST_IMAGE_TAG}" \
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

wait_while_pod_is_active() {
    timeout=${1}
    timeout "${timeout}" bash -c \
        'while kubectl -n '"${PROJECT_NAMESPACE}"' get pod '"${TEST_APP_PODNAME}"' -o jsonpath="{.status.phase}" | grep -q "Running" ; do sleep 1; done' || true
}

# Will run the test application and wait for its completion.
run_tests() {
    task_name=${1}
    timeout=${2}

    TEST_APP_PODNAME=mongodb-enterprise-operator-tests

    deploy_test_app

    wait_until_pod_is_running_or_failed_or_succeeded

    title "Running test ${task_name} (tag: ${TEST_IMAGE_TAG})"

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

dump_pods_logs() {
    i=1
    if ! kubectl get pods -n "${PROJECT_NAMESPACE}" 2>&1 | grep -q "No resources found"; then
        for pod in $(kubectl get pods -n ${PROJECT_NAMESPACE}  -o name | cut -d "/" -f 2 | grep -v "operator-"); do
            echo "Writing log file for pod ${pod} to logs/${pod}.log"
            kubectl logs -n ${PROJECT_NAMESPACE} ${pod} > "logs/${pod}.log"
            ((i++))
        done
    fi
    echo "${i} log files were written."
}

delete_mongodb_resources() {
    kubectl delete mdb --all  -n ${PROJECT_NAMESPACE}
}

initialize() {
    # Generic function to initialize anything we might need

    # Create a directory to store logs
    # Everything under this directory will be pushed to S3
    mkdir -p logs/

    if [ "${BUILD_VARIANT-}" = "e2e_openshift_origin_v3.11" ]; then
        title "More info: https://master.openshift-cluster.mongokubernetes.com:8443/console/project/${PROJECT_NAMESPACE}"
    fi

    # Make sure we use VERSION_ID if it is defined.
    if [[ -n "${VERSION_ID-}" ]]; then
        REVISION="${VERSION_ID}"
    fi
    export REVISION
}

initialize

if [[ "${MODE-}" != "dev" ]]; then
    if [ -n "${CURRENT_VERSION-}" ]; then
        REVISION="${CURRENT_VERSION}"
    fi

    redeploy_operator \
        "${REGISTRY}" \
        "${REVISION:-}" \
        "${PROJECT_NAMESPACE}" \
        "${WATCH_NAMESPACE:-$PROJECT_NAMESPACE}" \
        "Always" \
        "${MANAGED_SECURITY_CONTEXT:-}" \
        "2m"

    # Not required when running against the Ops Manager Kubernetes perpetual instance
    if [[ "${OM_EXTERNALLY_CONFIGURED:-}" != "true" ]]; then
        fetch_om_information
    fi

    configure_operator
fi

if [ -z "${TASK_NAME}" ]; then
    echo "TASK_NAME needs to be defined"
fi

TESTS_OK=0
run_tests "${TASK_NAME}" "${WAIT_TIMEOUT:-400}" || TESTS_OK=1

echo "Tests have finished with the following exit code: ${TESTS_OK}"

# In Evergreen we always clean namespaces if the test finished ok and dump diagnostic information otherwise.
# We don't do it for local development (as 'make e2e' will clean the resources before launching test)
if [[ "${MODE-}" != "dev" ]]; then
    if [[ "${TESTS_OK}" -eq 0 ]]; then
        kubectl label "namespace/${PROJECT_NAMESPACE}" "evg/state=pass" --overwrite=true

        if [[ "${OPERATOR_UPGRADE_IN_PROGRESS-}" = "stage1" ]]; then
            echo "Upgrade in progress, skipping removal of namespace"
        else
            teardown
        fi
    else
        # Dump diagnostic information
        dump_diagnostic_information "logs/diagnostics.txt"

        dump_pods_logs

        # Not required when running against the Ops Manager Kubernetes perpetual instance
        if [[ "${OM_EXTERNALLY_CONFIGURED:-}" != "true" ]]; then
            print_om_endpoint "${PROJECT_NAMESPACE}" "${OPS_MANAGER_NAMESPACE}" "${NODE_PORT}"
        else
            print_perpetual_om_endpoint "${PROJECT_NAMESPACE}"
        fi

        kubectl label "namespace/${PROJECT_NAMESPACE}" "evg/state=failed" --overwrite=true

        # we want to teardown no matter what if it's a static namespace in order to let other tests run
        # another case for always cleaning the namespace is Ops Manager tests - they consume too many resources and
        # it's too expensive to keep them
        if [[ -n ${STATIC_NAMESPACE-} ]] || [[ "${TEST_MODE:-}" = "opsmanager" ]]; then
            teardown
        fi
    fi
fi

[[ "${TESTS_OK}" -eq 0 ]]
