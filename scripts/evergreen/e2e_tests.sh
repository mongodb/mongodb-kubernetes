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

    title "ConfigMap and Secret are created"
}

teardown() {
    # Cluster maintenance should be the responsibility of other agent, not the test runner.
    # TODO: Use CronJob to start a command every hour to check for resources and delete
    # namespaces if needed
    # Make sure that under no circumstances, this function fails.

    kubectl delete mrs --all -n "${PROJECT_NAMESPACE}" || true
    kubectl delete mst --all -n "${PROJECT_NAMESPACE}" || true
    kubectl delete msc --all -n "${PROJECT_NAMESPACE}" || true
    echo "Removing test namespace"
    kubectl delete "namespace/${PROJECT_NAMESPACE}" || true
}

deploy_test_app() {
    title "Deploying test application"

    TEST_IMAGE_TAG=$(git rev-parse HEAD)
    JOB_NAME="job-e2e-tests"

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

    kubectl -n "${PROJECT_NAMESPACE}" apply -f mongodb-enterprise-tests.yaml
}

wait_while_job_has_not_started() {
    # If 'get pods' returns a No resources found, wait for a while
    # This can take as long as it needs to take, in case of a busy cluster, we have seen the test pods taking a few minutes to start
    while kubectl -n "${PROJECT_NAMESPACE}" get pods --selector=job-name=${JOB_NAME} 2>&1 | grep -q "No resources found" ; do sleep 3; done
}

wait_while_job_is_active() {
    timeout=${1}
    # .status.active is the state of the Job since it was created, before its Pod have reached completed state.
    # It will leave this state when the Pod finish, be it successfully or not.

    ( while [[ $(kubectl -n "${PROJECT_NAMESPACE}" get "jobs/${JOB_NAME}" -o jsonpath='{.status.active}') -eq 1 ]]; do sleep 3; done ) & pid=$!

    echo "Waiting while jobs/${JOB_NAME} is in .status.active state"
    wait_for_or_kill ${pid} "${timeout}"
}

# Will run the test application and wait for its completion.
run_tests() {
    test_name=${1}
    timeout=${2}

    deploy_test_app

    wait_while_job_has_not_started

    echo "this is the pod we should get"
    kubectl -n "${PROJECT_NAMESPACE}" get pods --selector=job-name="${JOB_NAME}" --output=jsonpath='{.items[*].metadata.name}'
    # At this point we should have a running Pod with the tests.
    TEST_APP_PODNAME=$(kubectl -n "${PROJECT_NAMESPACE}" get pods --selector=job-name="${JOB_NAME}" --output=jsonpath='{.items[*].metadata.name}')
    title "${test_name}.py (tag: ${TEST_IMAGE_TAG})"

    echo "We'll wait for jobs/${JOB_NAME} to complete"
    # Wait for job to be active up to 120 seconds
    if wait_while_job_is_active 120; then
        echo "plop "
    fi

    # we don't output logs to file when running tests locally
    if [[ "${MODE-}" == "dev" ]]; then
        kubectl -n "${PROJECT_NAMESPACE}" logs "${TEST_APP_PODNAME}"

        # sleeping so that in case of error manage to see the log
        sleep 3
    else
        output_filename="logs/test_app.log"
        operator_filename="logs/operator.log"

        # At this time both ${TEST_APP_PODNAME} have finished running, so we don't follow (-f) it
        # Similarly, the operator deployment has finished with our tests, so we print whatever we have
        # until this moment and go continue with our lives
        kubectl -n "${PROJECT_NAMESPACE}" logs "${TEST_APP_PODNAME}" > "${output_filename}"
        kubectl -n "${PROJECT_NAMESPACE}" logs "deployment/mongodb-enterprise-operator" > "${operator_filename}"

        # logs are saved in files (so we can upload them to s3), but also displayed in the evergreen gui
        cat "${output_filename}"
    fi

    [[ $(kubectl -n "${PROJECT_NAMESPACE}" get "jobs/${JOB_NAME}" --output=jsonpath='{.status.succeeded}') -eq 1 ]]
}

dump_agent_logs() {
    i=1

    for st in $(kubectl get mst -n "${PROJECT_NAMESPACE}" -o name | cut -d "/" -f 2); do
        obj="${st}-0"
        kubectl logs -n "${PROJECT_NAMESPACE}" "${obj}" > "logs/${obj}.log"
        ((i++))
    done

    for rs in $(kubectl get mrs -n "${PROJECT_NAMESPACE}" -o name | cut -d "/" -f 2); do
        for pod in $(kubectl get pods -n "${PROJECT_NAMESPACE}" -o name | grep "$rs" | cut -d "/" -f 2); do
            kubectl logs -n "${PROJECT_NAMESPACE}" "${pod}" > "logs/${pod}.log"
            ((i++))
        done
    done

    # let's dump only shard logs
    for sc in $(kubectl get msc -n "${PROJECT_NAMESPACE}" -o name | cut -d "/" -f 2); do
        for pod in $(kubectl get pods -n "${PROJECT_NAMESPACE}" -o name | grep "$sc" \
                    | grep -v "$sc-config" | grep -v "$sc-mongos" | cut -d "/" -f 2); do
            kubectl logs -n "${PROJECT_NAMESPACE}" "${pod}" > "logs/${pod}.log"

            ((i++))
        done
    done

    echo "${i} log files were written."
}

initialize() {
    # Generic function to initialize anything we might need

    # Create a directory to store logs
    # Everything under this directory will be pushed to S3
    mkdir -p logs/

    if [ "${BUILD_VARIANT}" = "e2e_openshift_origin_v3.11" ]; then
        title "More info: https://master.openshift-cluster.mongokubernetes.com:8443/console/project/${PROJECT_NAMESPACE}"
    fi
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

        # seems the removal of PVCs is the longest thing during later namespace cleanup in "prepare_test_env" - so let's
        # remove them now

        # Removing PVC from a running namespace, probably with still running mongods, can make it
        # difficult to debug problems, considering we are making even more noise with this removal.
        # I'll skip this part for now, the maintenance of the cluster is not the responsibility of
        # the test framework.
        # kubectl delete pvc --all -n "${PROJECT_NAMESPACE}" --now || true

        kubectl label "namespace/${PROJECT_NAMESPACE}" "evg/state=failed"
    fi
fi

[[ "${TESTS_OK}" -eq 0 ]]
