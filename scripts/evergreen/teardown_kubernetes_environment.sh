#!/usr/bin/env bash

set -Eeou pipefail

source scripts/dev/set_env_context.sh

if [ "${KUBE_ENVIRONMENT_NAME}" = "kind" ]; then
    echo "Deleting Kind cluster"
    kind delete clusters --all
fi
