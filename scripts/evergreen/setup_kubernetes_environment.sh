#!/usr/bin/env bash
set -Eeou pipefail

context_config="${workdir:?}/${kube_environment_name:?}_config"
bindir="${workdir}/bin"
if [ -f "${context_config}" ]; then
    echo "Context configuration already exist, host was not clearly cleaned up!"
    rm "${context_config}"

    exit 1
fi

if  [ "${kube_environment_name}" = "vanilla" ] || [ "${kube_environment_name}" = "multi" ]; then
    export AWS_ACCESS_KEY_ID="${mms_eng_test_aws_access_key:?}"
    export AWS_SECRET_ACCESS_KEY="${mms_eng_test_aws_secret:?}"
    export AWS_DEFAULT_REGION="${mms_eng_test_aws_region:?}"
    export KOPS_STATE_STORE=s3://kube-om-state-store

    echo "Downloading kops"
    curl -s -L https://github.com/kubernetes/kops/releases/download/v1.19.1/kops-linux-amd64 -o kops
    chmod +x kops
    mv kops "${bindir}"
fi

if [ "${kube_environment_name}" = "openshift_4" ]; then
    echo "Downloading OC & setting up Openshift 4 cluster"
    OC_PKG=oc-linux.tar.gz
    curl --fail --retry 3 -s -L https://mirror.openshift.com/pub/openshift-v4/clients/ocp/4.3.1/openshift-client-linux-4.3.1.tar.gz --output $OC_PKG
    tar xfz $OC_PKG &> /dev/null
    mv oc "${bindir}"

    # This uses a kubeconfig yaml file stored in the Evergreen project instead of the old token login
    # to avoid having to keep the tokens valid forever.
    echo "${openshift43_cluster_kubeconfig:?}" | base64 --decode > "${context_config}"

elif [ "${kube_environment_name}" = "vanilla" ]; then
    if [ -n "${cluster_name:-}" ]; then
        export CLUSTER=${cluster_name}
    else
        export CLUSTER=e2e.mongokubernetes.com
    fi

    if ! kops get clusters | grep -q $CLUSTER; then
        echo "Cluster $CLUSTER not found, exiting..."
        echo run "make recreate-e2e-kops imsure=yes cluster=$CLUSTER"
        kops get clusters
        exit 1
    fi

    kops export kubecfg $CLUSTER --admin=87600h
    mv "${HOME}"/.kube/config "${context_config}"

elif [ "${kube_environment_name}" = "kind" ]; then
    echo "Starting Kind"
    kubernetes_image="kindest/node:v1.21.1"
    kind create cluster \
        --image "${kubernetes_image}" \
        --kubeconfig "${context_config}"

elif [[ "${kube_environment_name}" = "minikube" ]]; then
    echo "Starting Minikube"
    minikube start --driver=docker --kubernetes-version=v1.16.15 --memory=50g &> /dev/null
    mv "${HOME}"/.kube/config "${context_config}"
elif [[ "${kube_environment_name}" = "multi" ]]; then

    # TODO: ensure that the clusters exist and are configured correctly.
    # shellcheck disable=SC2154
    kops export kubecfg "${central_cluster}" --admin=87600h
    # configure kube config with all member clusters
    # shellcheck disable=SC2154
    for member_cluster in ${member_clusters}; do
      kops export kubecfg "${member_cluster}" --admin=87600h
    done

    mv "${HOME}"/.kube/config "${context_config}"
else
    echo "kube_environment_name not recognized"
    echo "value is <<${kube_environment_name}>>. If empty it means it was not set"

    # Fail if there's no Kubernetes environment set
    exit 1
fi
