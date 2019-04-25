#!/usr/bin/env bash

set -euo pipefail

##
## Setups environment and runs e2e test
##

cd "$(git rev-parse --show-toplevel || echo "Failed to find git root"; exit 1)"

source scripts/funcs

# Will generate a random namespace to use each time
if [ -z "${PROJECT_NAMESPACE-}" ]; then
    random_namespace=$(LC_ALL=C tr -dc 'a-z0-9' </dev/urandom | head -c 20) || true
    doy=$(date +'%j')
    PROJECT_NAMESPACE="a-${doy}-${random_namespace}z"
    export PROJECT_NAMESPACE
    printf "Project Namespace is: %s\\n" "${PROJECT_NAMESPACE}"
else
    printf "Using %s namespace\\n" "${PROJECT_NAMESPACE}"
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
    title "Reading Ops Manager environment variables..."

    OPERATOR_TESTING_FRAMEWORK_NS=operator-testing
    if ! kubectl get namespace/${OPERATOR_TESTING_FRAMEWORK_NS} &> /dev/null; then
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
    title "Creating project and credentials Kubernetes object..."
    BASE_URL="${OM_BASE_URL:=http://ops-manager.operator-testing.svc.cluster.local:8080}"

    # delete `my-project` if it exists
    ! kubectl --namespace "${PROJECT_NAMESPACE}" get configmaps | grep -q my-project \
        || kubectl --namespace "${PROJECT_NAMESPACE}" delete configmap my-project
    # Configuring project
    kubectl --namespace "${PROJECT_NAMESPACE}" create configmap my-project \
            --from-literal=projectName="${PROJECT_NAMESPACE}" --from-literal=baseUrl="${BASE_URL}"

    # delete `my-credentials` if it exists
    ! kubectl --namespace "${PROJECT_NAMESPACE}" get secrets | grep -q my-credentials \
        || kubectl --namespace "${PROJECT_NAMESPACE}" delete secret my-credentials
    # Configure the Kubernetes credentials for Ops Manager
    kubectl --namespace "${PROJECT_NAMESPACE}" create secret generic my-credentials \
            --from-literal=user="${OM_USER:=admin}" --from-literal=publicApiKey="${OM_API_KEY}"


    title "Credentials, Project have been created"
}

teardown() {
    # Cluster maintenance should be the responsibility of other agent, not the test runner.
    # TODO: Use CronJob to start a command every hour to check for resources and delete
    # namespaces if needed
    # Make sure that under no circumstances, this function fails.

    kubectl delete mdb --all -n "${PROJECT_NAMESPACE}" || true
    echo "Removing test namespace"
    kubectl delete "namespace/${PROJECT_NAMESPACE}" || true
}

deploy_test_app() {
    title "Deploying test application"

    GIT_SHA=$(git rev-parse HEAD)

    # If running in evergreen, prefer the VERSION_ID to avoid GIT_SHA collisions
    # when people is building images from same commit.
    TEST_IMAGE_TAG="${VERSION_ID:-$GIT_SHA}"

    charttmpdir=$(mktemp -d 2>/dev/null || mktemp -d -t 'charttmpdir')
    charttmpdir=${charttmpdir}/chart
    cp -R "public/helm_chart/" "${charttmpdir}"

    cp scripts/evergreen/deployments/mongodb-enterprise-tests.yaml "${charttmpdir}/templates"

    pytest_addopts=""
    if [[ "${MODE-}" == "dev" ]]; then
        pytest_addopts="-s"
    fi

    # apply the correct configuration of the running OM instance
    helm template "${charttmpdir}" \
         -x templates/mongodb-enterprise-tests.yaml \
         --set repo="${REPO_URL:=268558157000.dkr.ecr.us-east-1.amazonaws.com/dev}" \
         --set baseUrl="${OM_BASE_URL:=http://ops-manager.operator-testing.svc.cluster.local:8080}" \
         --set apiKey="${OM_API_KEY}" \
         --set apiUser="${OM_USER:=admin}" \
         --set namespace="${PROJECT_NAMESPACE}" \
         --set testPath="${test_name}.py" \
         --set pytest.addopts="${pytest_addopts}" \
         --set tag="${TEST_IMAGE_TAG}" > mongodb-enterprise-tests.yaml || exit 1

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
    timeout ${timeout} bash -c \
        'while kubectl -n '"${PROJECT_NAMESPACE}"' get pod '"${TEST_APP_PODNAME}"' -o jsonpath="{.status.phase}" | grep -q "Running" ; do sleep 1; done' || true
}

# Will run the test application and wait for its completion.
run_tests() {
    test_name=${1}
    timeout=${2}

    TEST_APP_PODNAME=mongodb-enterprise-operator-tests

    deploy_test_app

    wait_until_pod_is_running_or_failed_or_succeeded

    title "Running test ${test_name}.py (tag: ${TEST_IMAGE_TAG})"

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

    [[ $(kubectl -n ${PROJECT_NAMESPACE} get pods/${TEST_APP_PODNAME} -o jsonpath='{.status.phase}') == "Succeeded" ]]
}

dump_agent_logs() {
    i=1
    for res in $(kubectl get mdb -n ${PROJECT_NAMESPACE} -o name | cut -d "/" -f 2); do
        for pod in $(kubectl get pods -n ${PROJECT_NAMESPACE}  -o name | grep "$res" | cut -d "/" -f 2 \
        | grep -v "$res-config" | grep -v "$res-mongos" | cut -d "/" -f 2); do # only dump shard logs if it's a sharded cluster
            kubectl logs -n ${PROJECT_NAMESPACE} ${pod} > "logs/${pod}"
            ((i++))
        done
    done
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

finalizer() {
    echo "*** Finalizer called"
}
trap finalizer SIGHUP SIGTERM

# sometimes in kops cluster some nodes get this taint that makes nodes non-schedulable. Just going over all nodes and
# trying to remove the taint is supposed to help
# (very view materials about this taint - this one https://github.com/kubernetes/kubernetes/blob/master/pkg/cloudprovider/providers/aws/aws.go#L204
# indicates that there are some problems with PVs, but removing PVs didn't help...)
fix_taints() {
    for n in $(kubectl get nodes -o name); do
        kubectl taint nodes "${n}" NodeWithImpairedVolumes:NoSchedule- &> /dev/null || true
    done
}

initialize

if [[ "${MODE-}" != "dev" ]]; then
    fix_taints

    redeploy_operator "268558157000.dkr.ecr.us-east-1.amazonaws.com/dev" \
            "${REVISION:-}" "${PROJECT_NAMESPACE}" "${WATCH_NAMESPACE:-$PROJECT_NAMESPACE}" "Always" "${MANAGED_SECURITY_CONTEXT:-}" "2m"

    # Not required when running against the Ops Manager Kubernetes perpetual instance
    if [[ "${USE_PERPETUAL_OPS_MANAGER_INSTANCE:-}" != "true" ]]; then
        fetch_om_information
    fi

    echo "Creating Operator Configuration for Ops Manager Test Instance."
    configure_operator
fi

if [ -z "${TEST_NAME}" ]; then
    echo "TEST_NAME needs to be defined"
fi

TESTS_OK=0
run_tests "${TEST_NAME}" "${WAIT_TIMEOUT:-400}" || TESTS_OK=1

echo "Tests have finished with the following exit code: ${TESTS_OK}"

# In Evergreen we always clean namespaces if the test finished ok and dump diagnostic information otherwise.
# We don't do it for local development (as 'make e2e' will clean the resources before launching test)
if [[ "${MODE-}" != "dev" ]]; then
    if [[ "${TESTS_OK}" -eq 0 ]]; then
        kubectl label "namespace/${PROJECT_NAMESPACE}" "evg/state=pass"

        teardown
    else
        # Dump diagnostic information
        dump_diagnostic_information "logs/diagnostics.txt"

        dump_agent_logs

        # Not required when running against the Ops Manager Kubernetes perpetual instance
        if [[ "${USE_PERPETUAL_OPS_MANAGER_INSTANCE:-}" != "true" ]]; then
            print_om_endpoint "${PROJECT_NAMESPACE}"
        else
            print_perpetual_om_endpoint "${PROJECT_NAMESPACE}"
        fi

        kubectl label "namespace/${PROJECT_NAMESPACE}" "evg/state=failed"
    fi
fi

[[ "${TESTS_OK}" -eq 0 ]]
