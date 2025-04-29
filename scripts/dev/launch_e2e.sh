#!/usr/bin/env bash

set -Eeou pipefail


# The script launches e2e test. Note, that the Operator and necessary resources are deployed
# inside the test

source scripts/dev/set_env_context.sh
source scripts/funcs/printing
source scripts/funcs/multicluster
source scripts/funcs/operator_deployment

export OM_BASE_URL=${OM_HOST}

# shellcheck disable=SC2154
title "Running the e2e test ${test}..."


if [[ "${OPS_MANAGER_REGISTRY}" == quay.io* ]]; then
    export OPS_MANAGER_NAME=mongodb-enterprise-ops-manager-ubi
fi
if [[ "${DATABASE_REGISTRY}" == quay.io* ]]; then
    export DATABASE_NAME=mongodb-kubernetes-database
fi

[[ ${skip:-} = "true" ]] && export SKIP_EXECUTION="'true'"

# If we are running this with local, it means we assume that the test is running on the local machine and not
# as a python script in a pod.
if [[ -n "${local:-}" ]]; then
    operator_context="$(kubectl config current-context)"
    if [[ "${KUBE_ENVIRONMENT_NAME:-}" = "multi" ]]; then
      prepare_multi_cluster_e2e_run
    fi

    prepare_operator_config_map "${operator_context}"

    pytest -m "${test}" docker/mongodb-enterprise-tests --disable-pytest-warnings

else
    TASK_NAME=${test} \
    WAIT_TIMEOUT="4m" \
    MODE="dev" \
    WATCH_NAMESPACE=${watch_namespace:-${NAMESPACE}} \
    REGISTRY=${REGISTRY} \
    DEBUG=${debug-} \
    ./scripts/evergreen/e2e/e2e.sh
fi

title "E2e test ${test} is finished"


