#!/usr/bin/env bash

set -eou pipefail
source scripts/dev/set_env_context.sh

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

function on_exit() {
  scripts/evergreen/e2e/dump_diagnostic_information_from_all_namespaces.sh
  cleanup
}

trap on_exit EXIT

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
