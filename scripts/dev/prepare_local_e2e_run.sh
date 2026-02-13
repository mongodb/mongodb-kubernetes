#!/usr/bin/env bash

set -Eeou pipefail

test "${MDB_BASH_DEBUG:-0}" -eq 1 && set -x

source scripts/dev/set_env_context.sh
source scripts/funcs/printing
source scripts/funcs/operator_deployment
source scripts/funcs/multicluster
source scripts/funcs/kubernetes

if [[ "$(uname)" == "Linux" ]]; then
  export PATH=/opt/golang/go1.25/bin:${PATH}
  export GOROOT=/opt/golang/go1.25
fi

on_exit() {
  # shellcheck disable=SC2181
  error_code=$?
  if [[ ${error_code} -ne 0 ]]; then
    echo
    echo "An error occurred during execution. Execute the script again."
    echo
    exit ${error_code}
  fi
}

trap on_exit EXIT
if [[ "${RESET:-"true"}" == "true" ]]; then
  echo "Running reset script..."
  go build -o "${PROJECT_DIR}/bin/reset" "${PROJECT_DIR}/scripts/dev/reset/"
  "${PROJECT_DIR}/bin/reset" 2>&1 | prepend "reset"
fi

current_context=$(kubectl config current-context)
# shellcheck disable=SC2154
if [[ "${KUBE_ENVIRONMENT_NAME}" == "multi" ]]; then
  current_context="${CENTRAL_CLUSTER}"
  (
    kubectl config set-context "${current_context}" "--namespace=${NAMESPACE}" &>/dev/null || true
    kubectl config use-context "${current_context}"
    echo "Current context: ${current_context}, namespace=${NAMESPACE}"
    kubectl get nodes | grep "control-plane"
  ) 2>&1 | prepend "set current context"
fi

echo "Ensuring namespace ${NAMESPACE}"
ensure_namespace "${NAMESPACE}" 2>&1 | prepend "ensure_namespace"

# Start independent make install and delete om project in background
(make install 2>&1 | prepend "make install") &
pid_install=$!
(scripts/dev/delete_om_projects.sh 2>&1 | prepend "delete_om_projects") &
pid_om=$!

echo "Configuring container auth (skips login if credentials still valid)"
scripts/dev/configure_container_auth.sh 2>&1 | prepend "configure_docker_auth"

echo "Configuring operator"
scripts/evergreen/e2e/configure_operator.sh 2>&1 | prepend "configure_operator"

echo "Preparing operator config map"
prepare_operator_config_map "$(kubectl config current-context)" 2>&1 | prepend "prepare_operator_config_map"

rm -rf docker/mongodb-kubernetes-tests/helm_chart
cp -rf helm_chart docker/mongodb-kubernetes-tests/helm_chart

# shellcheck disable=SC2154
if [[ "${KUBE_ENVIRONMENT_NAME}" == "multi" ]]; then
  go build -o "${PROJECT_DIR}/bin/prepare_multi_cluster" "${PROJECT_DIR}/scripts/dev/prepare-multi-cluster/"
  "${PROJECT_DIR}/bin/prepare_multi_cluster" 2>&1 | prepend "prepare_multi_cluster_e2e_run"
  run_multi_cluster_kube_config_creator 2>&1 | prepend "run_multi_cluster_kube_config_creator"
fi

# Wait for background operations before deploy step (which needs CRDs from make install)
wait "${pid_install}" || exit $?
wait "${pid_om}" || exit $?
test -f "docker/mongodb-kubernetes-tests/.test_identifiers" && rm "docker/mongodb-kubernetes-tests/.test_identifiers"

(
  if [[ "${DEPLOY_OPERATOR:-"false"}" == "true" ]]; then
    echo "installing operator helm chart to create the necessary sa and roles"
    # shellcheck disable=SC2178
    helm_values=$(get_operator_helm_values)
    # shellcheck disable=SC2179
    if [[ "${LOCAL_OPERATOR}" == true ]]; then
      helm_values+=" operator.replicas=0"
    fi

    # shellcheck disable=SC2128
  helm upgrade --install mongodb-kubernetes-operator helm_chart --set "$(echo "${helm_values}" | tr ' ' ',')"
  fi
) 2>&1 | prepend "deploy operator"

(
  if [[ "${KUBE_ENVIRONMENT_NAME}" == "kind" ]]; then
    echo "patching all default sa with imagePullSecrets to ensure we can deploy without setting it for each pod"

    service_accounts=$(kubectl get serviceaccounts -n "${NAMESPACE}" -o jsonpath='{.items[*].metadata.name}')

    for service_account in ${service_accounts}; do
      kubectl patch serviceaccount "${service_account}" -n "${NAMESPACE}" -p "{\"imagePullSecrets\": [{\"name\": \"image-registries-secret\"}]}"
    done
  fi
) 2>&1 | prepend "patch service accounts"
