#!/usr/bin/env bash

##
## Starts Ops Manager and setups environment for tests to run.
##

# Will generate a random namespace to use each time
if [ -z "${PROJECT_NAMESPACE}" ]; then
    random_namespace=$(LC_ALL=C tr -dc 'a-z0-9' </dev/urandom | head -c 13)
    PROJECT_NAMESPACE="test-${random_namespace}-ab"
    export PROJECT_NAMESPACE
    printf "Project Namespace is: %s\\n" "${PROJECT_NAMESPACE}"
else
    printf "Using %s namespace\\n" "${PROJECT_NAMESPACE}"
fi

# Array contains string; based on https://stackoverflow.com/questions/3685970/check-if-a-bash-array-contains-a-value
contains() {
    local e match=$1
    shift
    for e; do [[ "$e" == "$match" ]] && return 0; done
    return 1
}

spawn_om_kops() {
    echo "Starting Ops Manager Installation: $(date -u +'%Y-%m-%dT%H:%M:%SZ')"
    # Install the operator
    kubectl --namespace "${PROJECT_NAMESPACE}" apply -f deployments/mongodb-enterprise-ops-manager.yaml

    echo "* Waiting until Ops Manager is running..."
    while ! kubectl --namespace "${PROJECT_NAMESPACE}" get pods | grep 'mongodb-enterprise-ops-manager' | grep 'Running' >/dev/null; do sleep 1; done

    # wait for ops manager to really start
    echo "Ops Manager container is in Running state, waiting for Ops Manager to start."

    # We can't communicate with Ops Manager if it is inside Kubernetes, so we just
    # wait for this command to succeed.
    while ! kubectl --namespace "${PROJECT_NAMESPACE}" logs mongodb-enterprise-ops-manager-0 | grep 'Ops Manager ready' >/dev/null; do sleep 10; done

    # Get the environment from the ops-manager container
    eval $(kubectl -n "${PROJECT_NAMESPACE}" exec mongodb-enterprise-ops-manager-0 cat /opt/mongodb/mms/.ops-manager-env)
    echo "Finished Ops Manager Installation: $(date -u +'%Y-%m-%dT%H:%M:%SZ')"
}

install_operator() {
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
         -f deployments/values-test.yaml ../../public/helm_chart --output-dir "${outdir}"

    ls -l "${outdir}"
    cat "${outdir}"/*

    if [ ! $(kubectl get ns | grep -q "${PROJECT_NAMESPACE}") ]; then
        kubectl create ns "${PROJECT_NAMESPACE}"
        echo "Created namespace ${PROJECT_NAMESPACE} as it didn't exist"
    fi

    for file in crds roles serviceaccount operator
    do
        kubectl apply -f "helm_out/mongodb-enterprise-operator/templates/${file}.yaml"
    done
}

start_om_mci() {
    if [ ! -f omenv ]; then
        latest_vanilla=$(mci distros | grep vanilla | tail -n 1 | awk '{ print $3}')
        mci spawn "${latest_vanilla}"

        printf "Starting mci and giving it a few minutes to start."
        sleep 200
        OM_HOST="http://$(mci list | tail -n 1 | awk '{print $3}' | cut -d@ -f 2):8080"

        while ! curl -sL -m 2 -w "%{http_code}" "${OM_HOST}/user" -o /dev/null | grep -q 200; do printf "."; sleep 10; done
        echo

        printf "Configuring Ops Manager\\n"
        ../docker/mongodb-enterprise-ops-manager/scripts/configure-ops-manager.py "${OM_HOST}" omenv
    fi
    source omenv
}

configure_om() {
    # delete `my-project` if it exists
    ! kubectl --namespace "${PROJECT_NAMESPACE}" get configmaps | grep -q my-project || kubectl --namespace "${PROJECT_NAMESPACE}" delete configmap my-project
    # Configuring project
    kubectl --namespace "${PROJECT_NAMESPACE}" create configmap my-project --from-literal=projectName="operator-tests" --from-literal=baseUrl="${OM_HOST}"

    # delete `my-credentials` if it exists
    ! kubectl --namespace "${PROJECT_NAMESPACE}" get secrets | grep -q my-credentials || kubectl --namespace "${PROJECT_NAMESPACE}" delete secret my-credentials
    # Configure the Kubernetes credentials for Ops Manager
    kubectl --namespace "${PROJECT_NAMESPACE}" create secret generic my-credentials --from-literal=user="${OM_USER}" --from-literal=publicApiKey="${OM_API_KEY}"
}

configure_kubectl() {
    # this is the current cluster we are using for tests
    # it should be easy to have a list of clusters to use and to pick
    # one that's available.

    # aws client should be setup already for this to work.
    export KOPS_STATE_STORE=s3://dev02-mongokubernetes-com-state-store
    export CLUSTER=dev02.mongokubernetes.com

    echo "Exporting cluster configuration into kubectl"
    kops export kubecfg "${CLUSTER}"
}

test_locally() {
    setup_locally

    echo "Running tests"
    ./run.sh

    kubectl delete "ns/${PROJECT_NAMESPACE}"
}

setup_locally() {
    # This will spawn a new MCI OM instance and configure the env
    # First create mci instance
    start_om_mci

    # Install the operator in the currently configured context
    install_operator
    configure_om
}

init_kops_cluster() {
    export KOPS_STATE_STORE=s3://dev02-mongokubernetes-com-state-store
    export CLUSTER=dev02.mongokubernetes.com
    kops create cluster --node-count 3 --zones us-east-1a,us-east-1b,us-east-1c --node-size t2.large --master-size=t2.small  --kubernetes-version=v1.10.0 --ssh-public-key=~/.ssh/id_aws_rsa.pub --authorization RBAC $CLUSTER
    kops update cluster $CLUSTER --yes
}

setup_kubectl_environment() {
    # Install kubectl
    curl -LO https://storage.googleapis.com/kubernetes-release/release/v1.10.0/bin/linux/amd64/kubectl
    chmod +x kubectl
    mv kubectl "${BINDIR}"

    # Install kops
    curl -L https://github.com/kubernetes/kops/releases/download/1.9.1/kops-linux-amd64 -o kops
    chmod +x kops
    sudo mv kops "${BINDIR}"

    configure_kubectl
}

teardown() {
    printf "Removing namespace: %s\\n" "${PROJECT_NAMESPACE}."
    kubectl delete "namespace/${PROJECT_NAMESPACE}"
    echo "Removing Custom Resource Definitions."
    kubectl delete crd/mongodbreplicasets.mongodb.com crd/mongodbshardedclusters.mongodb.com crd/mongodbstandalones.mongodb.com
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
    echo "Image pushed to ECR."

    popd
}

install_helm() {
    # Installs helm if not previously installed
    if [ ! "$(which helm)" ]; then
        HELM=helm.tgz
        curl https://storage.googleapis.com/kubernetes-helm/helm-v2.10.0-linux-amd64.tar.gz --output ${HELM}
        tar xfz ${HELM}
        mv linux-amd64/helm "${BINDIR}"
        rm ${HELM}
    fi
}

run_tests() {
    test_stage="$1"

    TEST_IMAGE_TAG=$(git rev-parse HEAD)

    # create dummy helm chart
    charttmpdir=$(mktemp -d 2>/dev/null || mktemp -d -t 'charttmpdir')
    charttmpdir=${charttmpdir}/chart
    cp -R ../../public/helm_chart/ "${charttmpdir}"

    cp deployments/mongodb-enterprise-tests.yaml "${charttmpdir}/templates"
    # apply the correct configuration of the running OM instance
    helm template "${charttmpdir}" \
         -x templates/mongodb-enterprise-tests.yaml \
         --set apiKey="${OM_API_KEY}" \
         --set projectId="${OM_PROJECT_ID}" \
         --set namespace="${PROJECT_NAMESPACE}" \
         --set testStage="${test_stage}" \
         --set tag="${TEST_IMAGE_TAG}" > mongodb-enterprise-tests.yaml

    # Run the test container
    kubectl --namespace "${PROJECT_NAMESPACE}" apply -f mongodb-enterprise-tests.yaml

    sleep 10
    PODNAME=$(kubectl --namespace "${PROJECT_NAMESPACE}" get pods | grep mongodb-enterprise-operator-tests | head -n 1 | awk ' { print $1 } ')
    printf "Getting logs for %s.\\n" "${PODNAME}"

    output_filename="test_${PROJECT_NAMESPACE}_output.text"
    # Eventually this Pod will die (after run.sh has completed running), and this command will finish.
    kubectl --namespace "${PROJECT_NAMESPACE}" logs "${PODNAME}" -f > "${output_filename}" &
    KILLPID0=$!
    # Print the logs from the test container with a background tail.
    tail -f "${output_filename}" &
    KILLPID1=$!

    # Wait for as long as this Pod is Running.
    while kubectl --namespace "${PROJECT_NAMESPACE}" get "pod/${PODNAME}" | grep -q 'Running' ; do sleep 1; done

    # make sure there are not processes running in the background.
    kill $KILLPID0 $KILLPID1 &> /dev/null

    # After the pod has died check if it completed the tests successfuly.
    grep -q "ALL_TESTS_OK" "${output_filename}"
}

## Script options meant to run locally
if contains "init-cluster" "$@" ; then
    # Helper function to be used manually (for now)
    init_kops_cluster
    exit
fi

if contains "rebuild-test-image" "$@"; then
    rebuild_test_image
    exit
fi

if contains "teardown" "$@"; then
    teardown
    exit
fi

if contains "test-locally" "$@"; then
    ## Test locally with Ops Manager in MCI
    test_locally
    exit
fi

if contains "setup-locally" "$@"; then
    setup_locally
    exit
fi

## Normal run of script, to run tests, either locally or in Evergreen.
if [ ! -z "${IS_EVERGREEN}" ]; then
    # kubectl is needed to control the Kubernetes cluster.
    # it only needs to be installed in evergreen hosts, as the tool & config might not be there.
    echo "Setting up Kubectl environment."
    setup_kubectl_environment

    echo "Installing helm."
    install_helm
fi

echo "Installing Operator."
install_operator

echo "Installing Ops Manager."
spawn_om_kops

echo "Configuring Ops Manager."
configure_om

TESTS_OK=1
if contains "test-stage-replica-set-base" "$@"; then
    echo "Running Replica Set tests."
    run_tests "base"
    TESTS_OK=$?
    echo "Results of tests execution: ${TESTS_OK}"
fi

if contains "test-stage-replica-set-pv" "$@"; then
    echo "Running Replica Set with Persistent Volume tests."
    run_tests "with_pv"
    TESTS_OK=$?
    echo "Results of tests execution: ${TESTS_OK}"
fi

echo "Removing namespace"
teardown

if [ -z "${IS_EVERGREEN}" ]; then
    # During local runs, you might want to inspect the kubernetes cluster state
    # before tearing it down.
    printf "Tests have finished. Press ENTER to teardown test suite"
    read -r
fi

[ "${TESTS_OK}" -eq 0 ]
