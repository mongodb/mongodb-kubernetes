#!/usr/bin/env bash
set -Eeou pipefail

source scripts/dev/set_env_context.sh
source scripts/funcs/kubernetes

# shellcheck disable=SC2154
bindir="${PROJECT_DIR}/bin"

if [[ "${KUBE_ENVIRONMENT_NAME}" == "vanilla" || ("${KUBE_ENVIRONMENT_NAME}" == "multi" && "${CLUSTER_TYPE}" == "kops") ]]; then
    export AWS_ACCESS_KEY_ID="${mms_eng_test_aws_access_key:?}"
    export AWS_SECRET_ACCESS_KEY="${mms_eng_test_aws_secret:?}"
    export AWS_DEFAULT_REGION="${mms_eng_test_aws_region:?}"
fi

if [ "${KUBE_ENVIRONMENT_NAME}" = "openshift_4" ]; then
    echo "Downloading OC & setting up Openshift 4 cluster"
    OC_PKG=oc-linux.tar.gz

    # Source of this file is https://access.redhat.com/downloads/content/290/ver=4.12/rhel---8/4.12.8/x86_64/product-software
    # But it has been copied to S3 to avoid authentication issues in the future.
    curl --fail --retry 3 -s -L 'https://operator-kubernetes-build.s3.amazonaws.com/oc-4.12.8-linux.tar.gz' \
        --output "${OC_PKG}"
    tar xfz "${OC_PKG}" &>/dev/null
    mv oc "${bindir}"

    # https://stackoverflow.com/c/private-cloud-kubernetes/questions/15
    oc login --token="${OPENSHIFT_TOKEN}" --server="${OPENSHIFT_URL}"
elif [ "${KUBE_ENVIRONMENT_NAME}" = "kind" ] || [ "${KUBE_ENVIRONMENT_NAME}" = "performance" ]; then
    scripts/dev/recreate_kind_cluster.sh "kind"
elif [[ "${KUBE_ENVIRONMENT_NAME}" = "multi" && "${CLUSTER_TYPE}" == "kind" ]]; then
    scripts/dev/recreate_kind_clusters.sh
elif [[ "${KUBE_ENVIRONMENT_NAME}" = "minikube" ]]; then
    echo "Nothing to do for minikube"
else
    echo "KUBE_ENVIRONMENT_NAME not recognized"
    echo "value is <<${KUBE_ENVIRONMENT_NAME}>>. If empty it means it was not set"

    # Fail if there's no Kubernetes environment set
    exit 1
fi
