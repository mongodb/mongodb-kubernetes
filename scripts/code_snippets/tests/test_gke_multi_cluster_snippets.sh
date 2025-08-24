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
    ./public/architectures/setup-multi-cluster/setup-gke/teardown.sh
  elif [ "${code_snippets_reset:-false}" = true ]; then
      echo "Deleting resources, keeping the clusters"
      ./public/architectures/ops-manager-multi-cluster/teardown.sh &
      ./public/architectures/mongodb-sharded-multi-cluster/teardown.sh &
      ./public/architectures/mongodb-replicaset-multi-cluster/teardown.sh &
      wait

      ./public/architectures/setup-multi-cluster/setup-operator/teardown.sh
  else
    echo "Not deleting anything"
  fi
}
trap cleanup EXIT

source public/architectures/setup-multi-cluster/setup-gke/env_variables.sh
./public/architectures/setup-multi-cluster/setup-gke/test.sh

source public/architectures/setup-multi-cluster/setup-operator/env_variables.sh
./public/architectures/setup-multi-cluster/setup-operator/test.sh

./public/architectures/setup-multi-cluster/setup-istio/test.sh

./public/architectures/setup-multi-cluster/verify-connectivity/test.sh

./public/architectures/setup-multi-cluster/setup-cert-manager/test.sh

source public/architectures/ops-manager-multi-cluster/env_variables.sh
./public/architectures/ops-manager-multi-cluster/test.sh

source public/architectures/mongodb-replicaset-multi-cluster/env_variables.sh
./public/architectures/mongodb-replicaset-multi-cluster/test.sh

source public/architectures/mongodb-sharded-multi-cluster/env_variables.sh
./public/architectures/mongodb-sharded-multi-cluster/test.sh
