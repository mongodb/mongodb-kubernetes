#!/usr/bin/env bash

set -Eeou pipefail


# The script launches e2e test. Note, that the Operator and necessary resources are deployed
# inside the test

# shellcheck source=scripts/dev/set_env_context.sh
source scripts/dev/set_env_context.sh
# shellcheck source=scripts/funcs/printing
source scripts/funcs/printing


# This will allow to reuse the current namespace - it already has Operator installed
export PROJECT_NAMESPACE=${NAMESPACE}
export OM_BASE_URL=${OM_HOST}

# shellcheck disable=SC2154
title "Running the e2e test ${test}..."

if [[ ${CLUSTER_TYPE} = "openshift" ]]; then
    managed_security_context=true
fi

if [[ "${IMAGE_TYPE}" = "ubi" ]]; then
    if [[ "${OPS_MANAGER_REGISTRY}" == quay.io* ]]; then
      export OPS_MANAGER_NAME=mongodb-enterprise-ops-manager-ubi
    fi
    if [[ "${APPDB_REGISTRY}" == quay.io* ]]; then
      export APPDB_NAME=mongodb-enterprise-appdb-ubi
    fi
    if [[ "${DATABASE_REGISTRY}" == quay.io* ]]; then
      export DATABASE_NAME=mongodb-enterprise-database-ubi
    fi
fi

# For any cluster except for kops (Kind, Openshift) access to ECR registry needs authorization - it will be handled
# later in single_e2e.sh
if [[ ${CLUSTER_TYPE} != "kops" ]] && [[ ${REPO_URL} == *".ecr."* ]]; then
    export ecr_registry_needs_auth="ecr-registry-secret"
    ecr_registry="$(echo "${REPO_URL}" | cut -d "/" -f 1)"
    export ecr_registry
fi

[[ ${skip:-} = "true" ]] && export SKIP_EXECUTION="'true'"

if [[ -n "${local:-}" ]]; then
    pytest -m "${test}" docker/mongodb-enterprise-tests --disable-pytest-warnings
else
    # e2e test application doesn't update CRDs if they exist (as Helm 3 doesn't do this anymore)
    # so we need to make sure the CRDs are upgraded when run locally
    kubectl replace -f "public/helm_chart/crds" || kubectl apply -f "public/helm_chart/crds"

    TASK_NAME=${test} \
    WAIT_TIMEOUT="4m" \
    MODE="dev" \
    WATCH_NAMESPACE=${watch_namespace:-$PROJECT_NAMESPACE} \
    MANAGED_SECURITY_CONTEXT=${managed_security_context:-} \
    REGISTRY=${REPO_URL} \
    DEBUG=${debug-} \
    ./scripts/evergreen/e2e/e2e.sh
fi

title "E2e test ${test} is finished"


