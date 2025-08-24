#!/usr/bin/env bash

set -eou pipefail
source scripts/dev/set_env_context.sh

script_name=$(readlink -f "${BASH_SOURCE[0]}")

_SNIPPETS_OUTPUT_DIR="$(dirname "${script_name}")/outputs/$(basename "${script_name%.*}")"
export _SNIPPETS_OUTPUT_DIR
mkdir -p "${_SNIPPETS_OUTPUT_DIR}"

function cleanup() {
  if [ "${code_snippets_teardown:-true}" = true ]; then
    echo "Deleting clusters"
    ./public/architectures/setup-multi-cluster/ra-01-setup-gke/teardown.sh
  elif [ "${code_snippets_reset:-false}" = true ]; then
      echo "Deleting resources, keeping the clusters"
      ./public/architectures/ra-06-ops-manager-multi-cluster/teardown.sh &
      ./public/architectures/ra-08-mongodb-sharded-multi-cluster/teardown.sh &
      ./public/architectures/ra-07-mongodb-replicaset-multi-cluster/teardown.sh &
      wait

      ./public/architectures/setup-multi-cluster/ra-02-setup-operator/teardown.sh
  else
    echo "Not deleting anything"
  fi
}

dump_logs() {
  scripts/evergreen/e2e/dump_diagnostic_information_from_all_namespaces.sh "${K8S_CLUSTER_0_CONTEXT_NAME}"
  scripts/evergreen/e2e/dump_diagnostic_information_from_all_namespaces.sh "${K8S_CLUSTER_1_CONTEXT_NAME}"
  scripts/evergreen/e2e/dump_diagnostic_information_from_all_namespaces.sh "${K8S_CLUSTER_2_CONTEXT_NAME}"
}

cmd=${1:-""}
if [[ "${cmd}" == "dump_logs" ]]; then
  source public/architectures/setup-multi-cluster/ra-01-setup-gke/env_variables.sh
  dump_logs
  exit 0
elif [[ "${cmd}" == "cleanup" ]]; then
  source public/architectures/setup-multi-cluster/ra-01-setup-gke/env_variables.sh
  cleanup
  exit 0
fi
# if no cmd, proceed with the test normally
function on_exit() {
  dump_logs
  cleanup
}

trap on_exit EXIT


source public/architectures/setup-multi-cluster/ra-01-setup-gke/env_variables.sh
./public/architectures/setup-multi-cluster/ra-01-setup-gke/test.sh

source public/architectures/setup-multi-cluster/ra-02-setup-operator/env_variables.sh
./public/architectures/setup-multi-cluster/ra-02-setup-operator/test.sh

./public/architectures/setup-multi-cluster/ra-03-setup-istio/test.sh

./public/architectures/setup-multi-cluster/ra-04-verify-connectivity/test.sh

./public/architectures/setup-multi-cluster/ra-05-setup-cert-manager/test.sh

source public/architectures/ra-06-ops-manager-multi-cluster/env_variables.sh
./public/architectures/ra-06-ops-manager-multi-cluster/test.sh

source public/architectures/ra-07-mongodb-replicaset-multi-cluster/env_variables.sh
./public/architectures/ra-07-mongodb-replicaset-multi-cluster/test.sh

source public/architectures/ra-08-mongodb-sharded-multi-cluster/env_variables.sh
./public/architectures/ra-08-mongodb-sharded-multi-cluster/test.sh
