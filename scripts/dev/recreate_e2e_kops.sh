#!/usr/bin/env bash

set -Eeou pipefail

source scripts/dev/set_env_context.sh
source scripts/funcs/errors
source scripts/funcs/kubernetes
source scripts/funcs/printing

recreate="${1-}"
CLUSTER="${2:-e2e.mongokubernetes.com}"

title "Deleting kops cluster ${CLUSTER}"

if [[ "${recreate}" != "yes" ]]; then
	fatal "Exiting as \"imsure=yes\" parameter is not specified"
fi

# make sure kops version is >= 1.14.0
kops_version=$(kops version | awk '{ print $2 }')
major=$(echo "${kops_version}" | cut -d "." -f 1)
minor=$(echo "${kops_version}" | cut -d "." -f 2)
if (( major != 1 || minor < 14 )); then
        fatal "kops must be of version >= 1.14.0!"
fi


# wait until the cluster is removed (could be removed already)
kops delete cluster "${CLUSTER}" --yes || true

title "Cluster deleted"

# Note, that for e2e cluster we use us-east-2 region as us-east-1 most of all has reached max number of VPCs (5)
if [[ "${CLUSTER}" = "e2e.mongokubernetes.com" ]]; then
    # todo 2xlarge can be too big - this is a fix for the "2 OMs on one node" problem which should be solved by
    # pod anti affinity rule
    create_kops_cluster "${CLUSTER}" 4 64 "t2.2xlarge" "t2.medium" "us-east-2a,us-east-2b,us-east-2c"
elif [[ "${CLUSTER}" = "e2e.om.mongokubernetes.com" ]]; then
    # this one is for Ops Manager e2e tests
    create_kops_cluster "${CLUSTER}" 4 32 "t2.2xlarge" "t2.medium" "us-west-2a"
else [[ "${CLUSTER}" = "e2e.legacy.mongokubernetes.com" ]];
    # we're recreating a "legacy" cluster on K8s 1.11 to perform basic check.
    # This version is used by Openshift 3.11 and allows to more or less emulate 3.11 environment
    # Dev note: if you need to deploy Operator to this cluster you'll need to make two things before calling 'make'
    # 1. remove "subresources" field from each CRD
    # 2. remove "kubeVersion" field from Chart.yaml
    # TODO Ideally we should automatically run some tests on this cluster
    create_kops_cluster "${CLUSTER}" 2 16 "t2.medium" "t2.medium" "us-west-2a,us-west-2b,us-west-2c" "v1.11.10"
fi
