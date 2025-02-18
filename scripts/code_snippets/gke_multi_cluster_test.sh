#!/usr/bin/env bash

set -eou pipefail
source scripts/dev/set_env_context.sh

function cleanup() {
  if [ "${code_snippets_teardown:-true}" = true ]; then
    ./public/architectures/setup-multi-cluster/setup-gke/teardown.sh
  else
    echo "Not tearing down clusters"
  fi
}
trap cleanup EXIT

source public/architectures/setup-multi-cluster/setup-gke/env_variables.sh
./public/architectures/setup-multi-cluster/setup-gke/test.sh

./public/architectures/setup-multi-cluster/setup-istio/test.sh

./public/architectures/setup-multi-cluster/verify-connectivity/test.sh

source public/architectures/setup-multi-cluster/setup-operator/env_variables.sh
./public/architectures/setup-multi-cluster/setup-operator/test.sh

./public/architectures/setup-multi-cluster/setup-cert-manager/test.sh

source public/architectures/ops-manager-multi-cluster/env_variables.sh
./public/architectures/ops-manager-multi-cluster/test.sh

source public/architectures/mongodb-replicaset-multi-cluster/env_variables.sh
./public/architectures/mongodb-replicaset-multi-cluster/test.sh

source public/architectures/mongodb-sharded-multi-cluster/env_variables.sh
./public/architectures/mongodb-sharded-multi-cluster/test.sh
