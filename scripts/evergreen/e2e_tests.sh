#!/usr/bin/env bash

##
## Starts Ops Manager and setups environment for tests to run.
##
## To launch e2e scripts locally on predefined OM and operator initialize necessary env variables and launch the script
## providing test name:
##
## export ORG_ID="5bab96c432774481a41a4e67"
## export OM_BASE_URL="http://alisovenko.ngrok.io"
## export OM_API_KEY="f7f1d943-64b5-4557-90fa-f0250ec36d70"
## export OM_USER="alisovenko@gmail.com"
## TEST_STAGE=replica_set_pv_multiple e2e_tests.sh skip-ns-removal skip-installations rebuild-test-image
##

DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"
pushd $DIR
pwd

# Will generate a random namespace to use each time
if [ -z "${PROJECT_NAMESPACE}" ]; then
    random_namespace=$(LC_ALL=C tr -dc 'a-z0-9' </dev/urandom | head -c 20)
    doy=$(date +'%j')
    PROJECT_NAMESPACE="a-${doy}-${random_namespace}z"
    export PROJECT_NAMESPACE
    printf "Project Namespace is: %s\\n" "${PROJECT_NAMESPACE}"
else
    printf "Using %s namespace\\n" "${PROJECT_NAMESPACE}"
fi

# Create namespace if it doesn't exist yet
if ! kubectl get ns | grep -q "${PROJECT_NAMESPACE}"; then
    kubectl create ns "${PROJECT_NAMESPACE}"
    echo "Created namespace ${PROJECT_NAMESPACE} as it didn't exist"
fi


# Array contains string; based on https://stackoverflow.com/questions/3685970/check-if-a-bash-array-contains-a-value
contains() {
    local e match=$1
    shift
    for e; do [[ "$e" == "$match" ]] && return 0; done
    return 1
}

read_om_env() {
    OPERATOR_TESTING_FRAMEWORK_NS=operator-testing

    kubectl -n "${OPERATOR_TESTING_FRAMEWORK_NS}" exec mongodb-enterprise-ops-manager-0 ls "$1" && \
        eval $(kubectl -n "${OPERATOR_TESTING_FRAMEWORK_NS}" exec mongodb-enterprise-ops-manager-0 cat "$1")
}

spawn_om_kubernetes() {
    echo "##### Installing Ops Manager..."

    OPERATOR_TESTING_FRAMEWORK_NS=operator-testing
    if ! kubectl get namespace/${OPERATOR_TESTING_FRAMEWORK_NS} &> /dev/null; then
        echo "Ops Manager is not installed in this cluster. Doing it now."
        # If Ops Manager is not running, run it first!
        # Need to install Ops Manager
        kubectl create namespace "${OPERATOR_TESTING_FRAMEWORK_NS}"
        echo "Starting Ops Manager Installation: $(date -u +'%Y-%m-%dT%H:%M:%SZ')"
        # Install the operator
        kubectl --namespace "${OPERATOR_TESTING_FRAMEWORK_NS}" apply -f deployments/mongodb-enterprise-ops-manager.yaml

        echo "* Waiting until Ops Manager is running..."
        while ! kubectl --namespace "${OPERATOR_TESTING_FRAMEWORK_NS}" get pods/mongodb-enterprise-ops-manager-0 | grep -q 'Running'; do sleep 4; done

        # wait for ops manager to really start
        echo "Ops Manager container is in Running state, waiting for Ops Manager to start."
        # We can't communicate with Ops Manager if it is inside Kubernetes, so we just
        # wait for this command to succeed.
        while ! kubectl --namespace "${OPERATOR_TESTING_FRAMEWORK_NS}" get pods/mongodb-enterprise-ops-manager-0 -o jsonpath="{.status.containerStatuses[0].ready}" | grep -q "true"; do sleep 4; done

        echo "Ops Manager is installed in this cluster. A new user will be added for automated tests to run."
        sleep 10 # sleep for a few seconds so the user has time to be created.
    else
        echo "Ops Manager is already installed in this cluster. Will reuse it now."
        echo "If you want to start with a fresh Ops Manager installation, please delete the ${OPERATOR_TESTING_FRAMEWORK_NS} namespace."
    fi

    # Get the environment from the ops-manager container
    echo "Getting credentials from Ops Manager"

    # Gets the om-environment from one of the possible locations of the environment file
    read_om_env "/opt/mongodb/mms/env/.ops-manager-env" || read_om_env "/opt/mongodb/mms/.ops-manager-env" || exit 1

    echo "OM_USER: ${OM_USER}"
    echo "OM_PASSWORD: ${OM_PASSWORD}"
    echo "OM_API_KEY: ${OM_API_KEY}"
}

install_operator() {
    echo "##### Installing Operator..."
    outdir='helm_out'

    if [ -z "${REVISION}" ]; then
        # In case there's no revision (running locally), then use latest operator & database
        # The `latest` tag will only exist in development registry (ECR)
        REVISION=latest
    fi

    mkdir -p ${outdir}
    helm template \
         --set namespace="${PROJECT_NAMESPACE}" \
         --set operator.version="${REVISION}" \
         --set managedSecurityContext="${MANAGED_SECURITY_CONTEXT:-false}" \
         -f deployments/values-test.yaml ../../public/helm_chart --output-dir "${outdir}" || exit 1

    for file in roles serviceaccount operator
    do
        kubectl apply -f "helm_out/mongodb-enterprise-operator/templates/${file}.yaml"
    done

    # The CRD might not exist in a new cluster install.
    if ! kubectl get crd/mongodbreplicasets.mongodb.com > /dev/null ; then
        echo "Installing CRDs."
        kubectl apply -f "helm_out/mongodb-enterprise-operator/templates/crds.yaml"
    fi

    echo "Waiting until Operator gets to Running state..."
    max_wait=30
    # In parallel scenarios, when multiple tests are running at the same time, the operator can take more than a few seconds to be ready.
    # I'm increasing this value to 30 to see how it reacts from now on.
    while ! kubectl -n "${PROJECT_NAMESPACE}" get pods -l app=mongodb-enterprise-operator -o jsonpath='{.items[0].status.phase}' | grep -q "Running" ; do
        sleep 1
        max_wait=$((max_wait - 1))
        if [ $max_wait -eq 0 ]; then
            echo "(!!) Operator hasn't reached RUNNING state. The full yaml configuration for the pod is:"
            echo "------------------------------------------------------"
            kubectl -n "${PROJECT_NAMESPACE}" get pods -l app=mongodb-enterprise-operator -o yaml

            # Exit and signal an error, the operator should not take more than 30 seconds to start
            exit 1
        fi
    done

    echo "##### Operator installed successfully"
}

configure_operator() {
    echo "##### Creating project and credentials Kubernetes object..."
    BASE_URL="${OM_BASE_URL:=http://ops-manager-internal.operator-testing.svc.cluster.local:8080}"

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

    echo "Project ConfigMap:"
    echo "------------------------------------------------------"
    kubectl --namespace "${PROJECT_NAMESPACE}" get configmap/my-project -o yaml

    echo "Credentials Secret:"
    echo "------------------------------------------------------"
    kubectl --namespace "${PROJECT_NAMESPACE}" get secret/my-credentials -o yaml

    echo "##### ConfigMap and Secret are created"

}

init_kops_cluster() {
    export KOPS_STATE_STORE=s3://dev02-mongokubernetes-com-state-store
    export CLUSTER=dev02.mongokubernetes.com
    kops create cluster --node-count 3 --zones us-east-1a,us-east-1b,us-east-1c --node-size t2.large --master-size=t2.small  --kubernetes-version=v1.10.0 --ssh-public-key=~/.ssh/id_aws_rsa.pub --authorization RBAC $CLUSTER
    kops update cluster $CLUSTER --yes
}

teardown() {
    printf "Removing Namespace: %s\\n" "${PROJECT_NAMESPACE}."
    kubectl delete "namespace/${PROJECT_NAMESPACE}"
}

rebuild_test_image() {
    # Run this if necessary
    # eval $(aws ecr get-login --no-include-email --region us-east-1)
    pushd ../../docker/mongodb-enterprise-tests/
    TEST_IMAGE_TAG=$(git rev-parse HEAD)
    LOCAL_IMAGE="dev/mongodb-enterprise-tests:${TEST_IMAGE_TAG}"
    REMOTE_IMAGE="268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/mongodb-enterprise-tests:${TEST_IMAGE_TAG}"

    echo "Rebuilding test image: ${LOCAL_IMAGE}"
    docker build -t "${LOCAL_IMAGE}" .
    docker tag "${LOCAL_IMAGE}" "${REMOTE_IMAGE}"

    echo "Pushing Tag: ${REMOTE_IMAGE}"
    docker push "${REMOTE_IMAGE}"
    echo "mongodb-enterprise-tests image pushed to ECR."

    popd
}

run_tests() {
    test_stage="$1"

    TEST_IMAGE_TAG=$(git rev-parse HEAD)

    echo "-----------> Running test ${test_stage} \(tag: ${TEST_IMAGE_TAG}\)"

    echo "##### Deploying test application..."

    # create dummy helm chart
    charttmpdir=$(mktemp -d 2>/dev/null || mktemp -d -t 'charttmpdir')
    charttmpdir=${charttmpdir}/chart
    cp -R "${DIR}/../../public/helm_chart/" "${charttmpdir}"

    cp deployments/mongodb-enterprise-tests.yaml "${charttmpdir}/templates"
    # apply the correct configuration of the running OM instance
    helm template "${charttmpdir}" \
         -x templates/mongodb-enterprise-tests.yaml \
         --set baseUrl="${OM_BASE_URL:=http://ops-manager-internal.operator-testing.svc.cluster.local:8080}" \
         --set apiKey="${OM_API_KEY}" \
         --set apiUser="${OM_USER:=admin}" \
         --set namespace="${PROJECT_NAMESPACE}" \
         --set testStage="${test_stage}" \
         --set tag="${TEST_IMAGE_TAG}" > mongodb-enterprise-tests.yaml || exit 1

    kubectl --namespace "${PROJECT_NAMESPACE}" apply -f mongodb-enterprise-tests.yaml
    sleep 10

    PODNAME=mongodb-enterprise-operator-tests
    # Do wait while the Pod is not yet running (can be in Pending or ContainerCreating state)
    while ! kubectl --namespace "${PROJECT_NAMESPACE}" get "pod/${PODNAME}" | grep -q 'Running' ; do sleep 1; done

    # Eventually this Pod will finish, and this command will finish as well.
    kubectl --namespace "${PROJECT_NAMESPACE}" logs "${PODNAME}" -f

    # Get the output of the operator this time.
    echo "==== OPERATOR LOGS START ===="
    kubectl --namespace "${PROJECT_NAMESPACE}" logs "deployment/mongodb-enterprise-operator"
    echo "==== OPERATOR LOGS END ======"

    # The tests Pod might not be finish right away, so I wait until it is not in Running state anymore
    while kubectl --namespace "${PROJECT_NAMESPACE}" get "pod/${PODNAME}" | grep -q 'Running' ; do sleep 1; done
    [ "$(kubectl -n ${PROJECT_NAMESPACE} get pods/${PODNAME} -o jsonpath='{.status.phase}')" = "Succeeded" ]
}

## Script options meant to run locally
if contains "init-cluster" "$@" ; then
    # Helper function to be used manually (for now)
    init_kops_cluster
    exit
fi

if contains "rebuild-test-image" "$@"; then
    rebuild_test_image
fi

if ! contains "skip-installations" "$@"; then
    install_operator
fi

if ! contains "skip-installations" "$@"; then
    spawn_om_kubernetes
fi

echo "Creating Operator Configuration for Ops Manager Test Instance."
configure_operator

if [ -z "${TEST_STAGE}" ]; then
    echo "TEST_STAGE needs to be defined"
fi

run_tests "${TEST_STAGE}"
TESTS_OK=$?
echo "Tests have finished with the following exit code: ${TESTS_OK}"

if [ "${TESTS_OK}" -eq 0 ] && ! contains "skip-ns-removal" "$@"; then
    teardown
fi

if [ -z "${IS_EVERGREEN}" ]; then
    # During local runs, you might want to inspect the kubernetes cluster state
    # before tearing it down.
    printf "Tests have finished. Press ENTER to teardown test suite"
    read -r
fi

[ "${TESTS_OK}" -eq 0 ]
