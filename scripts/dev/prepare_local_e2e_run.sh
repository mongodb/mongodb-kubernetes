#!/bin/bash

set -Eeou pipefail
source scripts/dev/set_env_context.sh
source scripts/funcs/printing
source scripts/funcs/operator_deployment
source scripts/funcs/multicluster
source scripts/funcs/kubernetes

echo "Resetting"
scripts/dev/reset.sh

echo "Ensuring namespace ${NAMESPACE}"
ensure_namespace "${NAMESPACE}"

echo "Configuring operator"
scripts/evergreen/e2e/configure_operator.sh 2>&1 | prepend "configure_operator: "

echo "Preparing operator config map"
prepare_operator_config_map "$(kubectl config current-context)" 2>&1 | prepend "prepare_operator_config_map: "

rm -rf docker/mongodb-enterprise-tests/helm_chart
cp -r helm_chart docker/mongodb-enterprise-tests/helm_chart

# shellcheck disable=SC2154
if [[ "${kube_environment_name}" == "multi" ]]; then
  prepare_multi_cluster_e2e_run 2>&1 | prepend "prepare_multi_cluster_e2e_run"
  run_multi_cluster_kube_config_creator 2>&1 | prepend "run_multi_cluster_kube_config_creator"
fi

make install 2>&1 | prepend "make install: "
test -f "docker/mongodb-enterprise-tests/.test_identifiers" && rm "docker/mongodb-enterprise-tests/.test_identifiers"
scripts/dev/delete_om_projects.sh
echo "Deleting ~/.docker/.config.json and re-creating it"
rm ~/.docker/config.json
scripts/dev/configure_docker_auth.sh

echo "installing operator helm chart to create the necessary sa and roles"
helm_values=$(get_operator_helm_values)

# Conditionally append values to the helm_values variable
if [[ "$LOCAL_OPERATOR" == true ]]; then
  helm_values+=" operator.replicas=0"
fi

helm upgrade --install mongodb-enterprise-operator mongodb/enterprise-operator --set "$(echo "$helm_values" | tr ' ' ',')"

echo "patching default sa mongodb-enterprise-database-pods with imagePullSecrets to ensure we can deploy without setting it for each pod"
kubectl patch serviceaccount mongodb-enterprise-database-pods  \
  -p "{\"imagePullSecrets\": [{\"name\": \"image-registries-secret\"}]}" \
  -n "${PROJECT_NAMESPACE}"
