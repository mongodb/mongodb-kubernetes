#!/usr/bin/env bash
set -Eeou pipefail

source scripts/dev/set_env_context.sh
source scripts/funcs/kubernetes
source scripts/funcs/install
source scripts/funcs/binary_cache

# shellcheck disable=SC2154
bindir="${PROJECT_DIR}/bin"

if [[ "${KUBE_ENVIRONMENT_NAME}" == "vanilla" || ("${KUBE_ENVIRONMENT_NAME}" == "multi" && "${CLUSTER_TYPE}" == "minikube") ]]; then
    export AWS_ACCESS_KEY_ID="${mms_eng_test_aws_access_key:?}"
    export AWS_SECRET_ACCESS_KEY="${mms_eng_test_aws_secret:?}"
    export AWS_DEFAULT_REGION="${mms_eng_test_aws_region:?}"
fi

if [ "${KUBE_ENVIRONMENT_NAME}" = "openshift_4" ]; then
    echo "Downloading OC & setting up Openshift 4 cluster"

    # Initialize cache (if available)
    cache_available=false
    if init_cache_dir; then
        cache_available=true
    fi

    oc_version="4.12.8"
    if [[ "$cache_available" == "true" ]] && get_cached_binary "oc" "${oc_version}" "${bindir}/oc"; then
        echo "oc restored from cache"
    else
        OC_PKG=oc-linux.tar.gz

        # Source of this file is https://access.redhat.com/downloads/content/290/ver=4.12/rhel---8/4.12.8/x86_64/product-software
        # But it has been copied to S3 to avoid authentication issues in the future.
        curl_with_retry -s -L 'https://operator-kubernetes-build.s3.amazonaws.com/oc-4.12.8-linux.tar.gz' --output "${OC_PKG}"
        tar xfz "${OC_PKG}" &>/dev/null
        mv oc "${bindir}"
        rm -f "${OC_PKG}"
        if [[ "$cache_available" == "true" ]]; then
            cache_binary "oc" "${oc_version}" "${bindir}/oc"
        fi
    fi

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
