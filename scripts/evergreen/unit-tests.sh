#!/usr/bin/env bash

set -Eeou pipefail

source scripts/dev/set_env_context.sh

# shellcheck disable=SC2038
# shellcheck disable=SC2016
find . -name go.mod -exec dirname "{}" \+ | xargs -L 1 /bin/bash -o pipefail -c 'cd "$0" && echo "testing $0" && go test -coverprofile cover.out ./... | tee -a ops-manager-kubernetes.suite'
