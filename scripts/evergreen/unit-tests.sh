#!/usr/bin/env bash

set -Eeou pipefail

# shellcheck disable=SC2038
# shellcheck disable=SC2016
# TODO: MCK once we merge folder we need to update this
# This command iterates over all the dirs (excepting ./docker/mongodb-kubernetes-tests/*) that have go.mod in them
# and then `cd`s into those to run the go test command.
find . -name go.mod -not -path "./docker/mongodb-kubernetes-tests/*" -exec dirname "{}" \+ | xargs -L 1 /bin/bash -c '
cd "$0"
echo "testing $0"
rm -f result.suite
if [ "$USE_RACE" = "true" ]; then
  echo "running test with race enabled"
  GO_TEST_CMD="go test -v -coverprofile cover.out \$(go list ./... | grep -v \"mongodb-community-operator/test/e2e\")"
else
  echo "running test without race enabled"
  GO_TEST_CMD="go test -v -coverprofile cover.out \$(go list ./... | grep -v \"mongodb-community-operator/test/e2e\")"
fi
echo "running $GO_TEST_CMD"
eval "$GO_TEST_CMD" | tee -a result.suite
'
