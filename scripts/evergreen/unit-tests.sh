#!/usr/bin/env bash
# shellcheck disable=SC2044,SC2086

set -Eeou pipefail

# TODO: MCK once we merge folder we need to update this
# This command iterates over all the dirs (excepting ./docker/mongodb-kubernetes-tests/*) that have go.mod in them
# and then `cd`s into those to run the go test command.
# GOEXPERIMENT=synctest enables the testing/synctest package for fake-time testing
export GOEXPERIMENT=synctest

find . -name go.mod -not -path "./docker/mongodb-kubernetes-tests/*" -exec dirname "{}" \+ | while read -r dir; do
  cd "${dir}"
  echo "testing ${dir}"
  rm -f result.suite

  PACKAGES=$(go list ./... | grep -v "mongodb-community-operator/test/e2e")

  if [ "${USE_RACE:-}" = "true" ]; then
    echo "running test with race enabled"
    go test -race -v -coverprofile cover.out ${PACKAGES} | tee -a result.suite
  else
    echo "running test without race enabled"
    go test -v -coverprofile cover.out ${PACKAGES} | tee -a result.suite
  fi
  cd - > /dev/null
done
