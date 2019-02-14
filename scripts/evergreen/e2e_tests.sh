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
        echo "Ops Manager is not installed in this cluster. Make sure the Ops Manager installation script is called beforehand. Exiting..."

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

    header "Project ConfigMap:"
    kubectl --namespace "${PROJECT_NAMESPACE}" get configmap/my-project -o yaml

    header "Credentials Secret:"
    kubectl --namespace "${PROJECT_NAMESPACE}" get secret/my-credentials -o yaml

    title "ConfigMap and Secret are created"

}

teardown() {
    kubectl delete mrs --all -n ${PROJECT_NAMESPACE}
    kubectl delete mst --all -n ${PROJECT_NAMESPACE}
    kubectl delete msc --all -n ${PROJECT_NAMESPACE}
    printf "Removing Namespace: %s\\n" "${PROJECT_NAMESPACE}."
    kubectl delete "namespace/${PROJECT_NAMESPACE}"
}

deploy_test_app() {
    title "Deploying test application..."

    # create dummy helm chart
    charttmpdir=$(mktemp -d 2>/dev/null || mktemp -d -t 'charttmpdir')
    charttmpdir=${charttmpdir}/chart
    cp -R "public/helm_chart/" "${charttmpdir}"

    cp scripts/evergreen/deployments/mongodb-enterprise-tests.yaml "${charttmpdir}/templates"

    # apply the correct configuration of the running OM instance
    helm template "${charttmpdir}" \
         -x templates/mongodb-enterprise-tests.yaml \
         --set repo="${REPO_URL:=268558157000.dkr.ecr.us-east-1.amazonaws.com/dev}" \
         --set baseUrl="${OM_BASE_URL:=http://ops-manager.operator-testing.svc.cluster.local:8080}" \
         --set apiKey="${OM_API_KEY}" \
         --set apiUser="${OM_USER:=admin}" \
         --set namespace="${PROJECT_NAMESPACE}" \
         --set testPath="${test_name}.py" \
         --set tag="${TEST_IMAGE_TAG}" > mongodb-enterprise-tests.yaml || exit 1

    kubectl -n "${PROJECT_NAMESPACE}" delete -f mongodb-enterprise-tests.yaml || true

    kubectl -n "${PROJECT_NAMESPACE}" apply -f mongodb-enterprise-tests.yaml

    title "Deployed test application, waiting until it gets ready..."

    # Do wait while the Pod is not yet running (can be in Pending or ContainerCreating state)
    timeout "20s" bash -c \
        'while ! kubectl -n '"${PROJECT_NAMESPACE}"' get pod '"${TEST_APP_PODNAME}"' -o jsonpath="{.status.phase}" | grep -q "Running" ; do printf .; sleep 1; done;' || true

    echo

    if ! kubectl -n "${PROJECT_NAMESPACE}" get pod ${TEST_APP_PODNAME} -o jsonpath="{.status.phase}" | grep -q "Running"; then
        status=$(kubectl -n "${PROJECT_NAMESPACE}" get pod ${TEST_APP_PODNAME} -o jsonpath="{.status.phase}")
        error "Test application failed to reach Running state"

        header "Output from \"kubectl describe ${TEST_APP_PODNAME}\":"
        kubectl -n "${PROJECT_NAMESPACE}" describe pod ${TEST_APP_PODNAME}

        header "Test application logs:"
        kubectl -n "${PROJECT_NAMESPACE}" logs ${TEST_APP_PODNAME}

        echo
        title "Test application didn't start (status: $status), exiting..."
        exit 1
    fi

    title "Test application is ready"

}

# Deploys the test application and waits until it finishes. The test application runs the pytest with specific scenario
# Note, that the script doesn't build the test docker image - it must be done by the client beforehand
run_tests() {
    test_name="$1"
    timeout="$2"

    TEST_IMAGE_TAG=$(git rev-parse HEAD)

    echo "-----------> Running test ${test_name}.py \(tag: ${TEST_IMAGE_TAG}\)"

    TEST_APP_PODNAME=mongodb-enterprise-operator-tests

    deploy_test_app

    # we don't output logs to file when running tests locally
    if [[ "${MODE-}" == "dev" ]]; then
        kubectl -n "${PROJECT_NAMESPACE}" logs "${TEST_APP_PODNAME}" -f &
        KILLPID0=$!

        trap "kill $KILLPID0 &> /dev/null || true"  SIGINT SIGTERM SIGQUIT

        # sleeping so that in case of error manage to see the log
        sleep 3
    else
        output_filename="test_app.log"
        operator_filename="operator.log"

        # Eventually this Pod will die (after run.sh has completed running), and this command will finish.
        kubectl -n "${PROJECT_NAMESPACE}" logs "${TEST_APP_PODNAME}" -f > "${output_filename}" &
        KILLPID0=$!
        kubectl -n "${PROJECT_NAMESPACE}" logs "deployment/mongodb-enterprise-operator" -f > "${operator_filename}" &
        KILLPID1=$!
        # Print the logs from the test container with a background tail.
        tail -f "${output_filename}" &
        KILLPID2=$!

        trap "kill $KILLPID0 $KILLPID1 $KILLPID2  &> /dev/null || true"  SIGINT SIGTERM SIGQUIT
    fi

    # Note, that we wait for 8 minutes maximum - this is less than the ultimate evergreen task timeout (10 minutes) as
    # we want to dump diagnostic information in case of failure
    timeout ${timeout} bash -c \
        'while kubectl -n '"${PROJECT_NAMESPACE}"' get pod '"${TEST_APP_PODNAME}"' -o jsonpath="{.status.phase}" | grep -q "Running" ; do sleep 1; done' || true

    # make sure there are not processes running in the background.
    kill $KILLPID0 2>/dev/null || true
    if [[ "${MODE-}" != "dev" ]]; then
        kill $KILLPID1 $KILLPID2 &> /dev/null
    fi

    if [[ "$(kubectl -n ${PROJECT_NAMESPACE} get pods/${TEST_APP_PODNAME} -o jsonpath='{.status.phase}')" != "Succeeded" ]]; then
        test_app_status="$(kubectl -n ${PROJECT_NAMESPACE} get pods/${TEST_APP_PODNAME} -o jsonpath='{.status.phase}')"
        error "Test application has status \"$test_app_status\" instead of \"Succeeded\""
        return 1
    fi
    return 0
}

dump_agent_logs() {
    agent_log_file="agent"
    i=1

    for st in $(kubectl get mst -n ${PROJECT_NAMESPACE} -o name | cut -d "/" -f 2); do
        filename=${agent_log_file}${i}".log"
        kubectl logs -n ${PROJECT_NAMESPACE} "${st}-0" > ${filename}

        echo "Wrote ${st}-0 logs to file ${filename}"
        ((i++))
    done

    for rs in $(kubectl get mrs -n ${PROJECT_NAMESPACE} -o name | cut -d "/" -f 2); do
        for pod in $(kubectl get pods -n ${PROJECT_NAMESPACE} -o name | grep "$rs" | cut -d "/" -f 2); do
            filename=${agent_log_file}${i}".log"
            kubectl logs -n ${PROJECT_NAMESPACE} ${pod} > ${filename}

            echo "Wrote ${pod} logs to file ${filename}"
            ((i++))
        done
    done

    # let's dump only shard logs
    for sc in $(kubectl get msc -n ${PROJECT_NAMESPACE} -o name | cut -d "/" -f 2); do
        for pod in $(kubectl get pods -n ${PROJECT_NAMESPACE} -o name | grep "$sc" \
                    | grep -v "$sc-config" | grep -v "$sc-mongos" | cut -d "/" -f 2); do
            filename=${agent_log_file}${i}".log"
            kubectl logs -n ${PROJECT_NAMESPACE} ${pod} > ${filename}

            echo "Wrote ${pod} logs to file ${filename}"
            ((i++))
        done
    done

}

delete_mongodb_resources() {
    kubectl delete mrs --all  -n ${PROJECT_NAMESPACE}
    kubectl delete mst --all  -n ${PROJECT_NAMESPACE}
    kubectl delete msc --all  -n ${PROJECT_NAMESPACE}
}

# sometimes in kops cluster some nodes get this taint that makes nodes non-schedulable. Just going over all nodes and
# trying to remove the taint is supposed to help
# (very view materials about this taint - this one https://github.com/kubernetes/kubernetes/blob/master/pkg/cloudprovider/providers/aws/aws.go#L204
# indicates that there are some problems with PVs, but removing PVs didn't help...)
fix_taints() {
    for n in $(kubectl get nodes -o name); do
        kubectl taint nodes ${n} NodeWithImpairedVolumes:NoSchedule- || true
    done
}
# foo
if [[ "${MODE-}" != "dev" ]]; then
    fix_taints

    redeploy_operator "268558157000.dkr.ecr.us-east-1.amazonaws.com/dev" \
            "${REVISION:-}" "${PROJECT_NAMESPACE}" "Always" "${MANAGED_SECURITY_CONTEXT:-}" "2m"

    fetch_om_information

    echo "Creating Operator Configuration for Ops Manager Test Instance."
    configure_operator
fi

if [ -z "${TEST_NAME}" ]; then
    echo "TEST_NAME needs to be defined"
fi


TESTS_OK=0

run_tests "${TEST_NAME}" "${WAIT_TIMEOUT:-8m}" || TESTS_OK=1

echo "Tests have finished with the following exit code: ${TESTS_OK}"

# In Evergreen we always clean namespaces if the test finished ok and dump diagnostic information otherwise.
# We don't do it for local development (as 'make e2e' will clean the resources before launching test)
if [[ "${MODE-}" != "dev" ]]; then
    if [[ "${TESTS_OK}" -eq 0 ]]; then
        teardown
    else
        # Dump diagnostic information
        dump_diagnostic_information diagnostics.txt

        dump_agent_logs

        print_om_endpoint "${PROJECT_NAMESPACE}"
    fi
fi

[[ "${TESTS_OK}" -eq 0 ]]
