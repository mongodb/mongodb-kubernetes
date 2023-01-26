#!/bin/bash

set -Eeoux pipefail
source scripts/dev/set_env_context.sh
source scripts/funcs/printing
source scripts/funcs/operator_deployment
source scripts/funcs/multicluster

make reset
scripts/dev/configure_operator.sh
prepare_operator_config_map "$(kubectl config current-context)"

rm -rf docker/mongodb-enterprise-tests/helm_chart
cp -r helm_chart docker/mongodb-enterprise-tests/helm_chart

# shellcheck disable=SC2154
if [[ "${kube_environment_name}" == "multi" ]]; then
  prepare_multi_cluster_e2e_run
  run_multi_cluster_kube_config_creator
fi

make install
test -f "docker/mongodb-enterprise-tests/.test_identifiers" && rm "docker/mongodb-enterprise-tests/.test_identifiers"

