#!/usr/bin/env bash

# shellcheck disable=SC2038
# shellcheck disable=SC2016
find . -name go.mod -exec dirname "{}" \+ | xargs -L 1 /bin/bash -o pipefail -c 'cd "$0" && echo "testing $0" && go test -coverprofile cover.out ./... | tee -a ops-manager-kubernetes.suite'
