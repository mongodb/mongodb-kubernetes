#!/usr/bin/env bash

set -Eeou pipefail

# shellcheck disable=SC2038
# shellcheck disable=SC2016
# TODO: MCK once we merge folder we need to update this
# This command iterates over all the dirs (excepting ./docker/mongodb-kubernetes-tests/*) that have go.mod in them
# and then `cd`s into those to run the go test command.
# GOEXPERIMENT=synctest enables the testing/synctest package for fake-time testing

# Debug: show Go version and environment
echo "Go version: $(go version)"
echo "GOROOT: $(go env GOROOT)"
echo "GOEXPERIMENT before setting: $(go env GOEXPERIMENT)"

# Set GOEXPERIMENT persistently using go env
go env -w GOEXPERIMENT=synctest
export GOEXPERIMENT=synctest

# Unset any GOFLAGS that might interfere
unset GOFLAGS

echo "GOEXPERIMENT after setting: $(go env GOEXPERIMENT)"
echo "GOFLAGS: $GOFLAGS"

# Clear Go build cache to avoid stale cache issues with GOEXPERIMENT
echo "Clearing Go build cache..."
go clean -cache

echo "Checking if synctest is available..."
ls -la "$(go env GOROOT)/src/testing/synctest/" || echo "synctest directory not found"

for dir in $(find . -name go.mod -not -path "./docker/mongodb-kubernetes-tests/*" -exec dirname "{}" \+); do
  cd "$dir"
  echo "testing $dir"
  rm -f result.suite

  # Get package list first (GOEXPERIMENT needed for parsing files that import testing/synctest)
  PACKAGES=$(GOEXPERIMENT=synctest go list ./... | grep -v "mongodb-community-operator/test/e2e")

  if [ "$USE_RACE" = "true" ]; then
    echo "running test with race enabled"
    GOEXPERIMENT=synctest go test -race -v -coverprofile cover.out $PACKAGES | tee -a result.suite
  else
    echo "running test without race enabled"
    GOEXPERIMENT=synctest go test -v -coverprofile cover.out $PACKAGES | tee -a result.suite
  fi
  cd - > /dev/null
done
