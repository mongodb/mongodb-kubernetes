#!/usr/bin/env bash

set -eou pipefail
source scripts/dev/set_env_context.sh

script_name=$(readlink -f "${BASH_SOURCE[0]}")

_SNIPPETS_OUTPUT_DIR="$(dirname "${script_name}")/outputs/$(basename "${script_name%.*}")"
export _SNIPPETS_OUTPUT_DIR
mkdir -p "${_SNIPPETS_OUTPUT_DIR}"

function cleanup() {
  if [ "${code_snippets_teardown:-true}" = true ]; then
    echo "Deleting clusters and resources"
    ./public/architectures/ops-manager-mc-no-mesh/teardown.sh &
    ./public/architectures/setup-multi-cluster/setup-externaldns/teardown.sh &
    wait

    ./public/architectures/setup-multi-cluster/setup-gke/teardown.sh
  elif [ "${code_snippets_reset:-false}" = true ]; then
      echo "Deleting resources, keeping the clusters"
      ./public/architectures/ops-manager-mc-no-mesh/teardown.sh &
      ./public/architectures/mongodb-sharded-mc-no-mesh/teardown.sh &
      ./public/architectures/mongodb-replicaset-mc-no-mesh/teardown.sh &
      ./public/architectures/setup-multi-cluster/setup-externaldns/teardown.sh &
      wait

      ./public/architectures/setup-multi-cluster/setup-operator/teardown.sh
  else
    echo "Not deleting anything"
  fi
}
trap cleanup EXIT

# store all outputs in


source public/architectures/setup-multi-cluster/setup-gke/env_variables.sh
./public/architectures/setup-multi-cluster/setup-gke/test.sh

source public/architectures/setup-multi-cluster/setup-operator/env_variables.sh
./public/architectures/setup-multi-cluster/setup-operator/test.sh

./public/architectures/setup-multi-cluster/setup-cert-manager/test.sh

source public/architectures/setup-multi-cluster/setup-externaldns/env_variables.sh
./public/architectures/setup-multi-cluster/setup-externaldns/test.sh

source public/architectures/ops-manager-mc-no-mesh/env_variables.sh
./public/architectures/ops-manager-mc-no-mesh/test.sh

source public/architectures/mongodb-replicaset-mc-no-mesh/env_variables.sh
./public/architectures/mongodb-replicaset-mc-no-mesh/test.sh

source public/architectures/mongodb-sharded-mc-no-mesh/env_variables.sh
./public/architectures/mongodb-sharded-mc-no-mesh/test.sh
