#!/bin/bash

set -Eeou pipefail

source scripts/dev/set_env_context.sh
source scripts/funcs/printing
source scripts/funcs/operator_deployment
source scripts/funcs/multicluster
source scripts/funcs/kubernetes

if [[ "$(uname)" == "Linux" ]]; then
  export PATH=/opt/golang/go1.21/bin:${PATH}
  export GOROOT=/opt/golang/go1.21
fi

if [[ "${RESET:-"true"}" == "true" ]]; then
  echo "Resetting"
  scripts/dev/reset.sh
fi

echo "Ensuring namespace ${NAMESPACE}"
ensure_namespace "${NAMESPACE}"

echo "Deleting ~/.docker/.config.json and re-creating it"
rm ~/.docker/config.json || true
scripts/dev/configure_docker_auth.sh


echo "Configuring operator"
scripts/evergreen/e2e/configure_operator.sh 2>&1 | prepend "configure_operator: "

echo "Preparing operator config map"
prepare_operator_config_map "$(kubectl config current-context)" 2>&1 | prepend "prepare_operator_config_map: "

rm -rf docker/mongodb-enterprise-tests/helm_chart
cp -r helm_chart docker/mongodb-enterprise-tests/helm_chart

# shellcheck disable=SC2154
if [[ "${KUBE_ENVIRONMENT_NAME}" == "multi" ]]; then
  prepare_multi_cluster_e2e_run 2>&1 | prepend "prepare_multi_cluster_e2e_run"
  run_multi_cluster_kube_config_creator 2>&1 | prepend "run_multi_cluster_kube_config_creator"
fi

make install 2>&1 | prepend "make install: "
test -f "docker/mongodb-enterprise-tests/.test_identifiers" && rm "docker/mongodb-enterprise-tests/.test_identifiers"
scripts/dev/delete_om_projects.sh


if [[ "${DEPLOY_OPERATOR:-"false"}" == "true" ]]; then
  echo "installing operator helm chart to create the necessary sa and roles"
  helm_values=$(get_operator_helm_values)
  if [[ "$LOCAL_OPERATOR" == true ]]; then
    helm_values+=" operator.replicas=0"
  fi

  helm upgrade --install mongodb-enterprise-operator helm_chart --set "$(echo "$helm_values" | tr ' ' ',')"
fi

if [[ "$KUBE_ENVIRONMENT_NAME" == "kind" ]]; then
  echo "patching all default sa with imagePullSecrets to ensure we can deploy without setting it for each pod"

  service_accounts=$(kubectl get serviceaccounts -n "${NAMESPACE}" -o jsonpath='{.items[*].metadata.name}')

  for service_account in $service_accounts; do
    kubectl patch serviceaccount "$service_account" -n "${NAMESPACE}" -p "{\"imagePullSecrets\": [{\"name\": \"image-registries-secret\"}]}"
  done
fi

# Generating om version mapping from release.json
cat release.json | jq -r '.supportedImages."mongodb-agent".opsManagerMapping' > .generated/om_version_mapping.json
