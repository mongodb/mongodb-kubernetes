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
    ./public/architectures/ra-10-ops-manager-mc-no-mesh/teardown.sh &
    ./public/architectures/setup-multi-cluster/ra-09-setup-externaldns/teardown.sh &
    wait

    ./public/architectures/setup-multi-cluster/ra-01-setup-gke/teardown.sh
  elif [ "${code_snippets_reset:-false}" = true ]; then
      echo "Deleting resources, keeping the clusters"
      ./public/architectures/ra-10-ops-manager-mc-no-mesh/teardown.sh &
      ./public/architectures/ra-11-mongodb-sharded-mc-no-mesh/teardown.sh &
      ./public/architectures/ra-12-mongodb-replicaset-mc-no-mesh/teardown.sh &
      ./public/architectures/setup-multi-cluster/ra-09-setup-externaldns/teardown.sh &
      wait

      ./public/architectures/setup-multi-cluster/ra-02-setup-operator/teardown.sh
  else
    echo "Not deleting anything"
  fi
}

function on_exit() {
  scripts/evergreen/e2e/dump_diagnostic_information_from_all_namespaces.sh
  cleanup
}

trap on_exit EXIT

# store all outputs in


source public/architectures/setup-multi-cluster/ra-01-setup-gke/env_variables.sh
./public/architectures/setup-multi-cluster/ra-01-setup-gke/test.sh

source public/architectures/setup-multi-cluster/ra-02-setup-operator/env_variables.sh
./public/architectures/setup-multi-cluster/ra-02-setup-operator/test.sh

./public/architectures/setup-multi-cluster/ra-05-setup-cert-manager/test.sh

source public/architectures/setup-multi-cluster/ra-09-setup-externaldns/env_variables.sh
./public/architectures/setup-multi-cluster/ra-09-setup-externaldns/test.sh

source public/architectures/ra-10-ops-manager-mc-no-mesh/env_variables.sh
./public/architectures/ra-10-ops-manager-mc-no-mesh/test.sh

source public/architectures/ra-12-mongodb-replicaset-mc-no-mesh/env_variables.sh
./public/architectures/ra-12-mongodb-replicaset-mc-no-mesh/test.sh

source public/architectures/ra-11-mongodb-sharded-mc-no-mesh/env_variables.sh
./public/architectures/ra-11-mongodb-sharded-mc-no-mesh/test.sh
