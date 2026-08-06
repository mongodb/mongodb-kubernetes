#!/usr/bin/env bash

set -Eeou pipefail

set -x

source scripts/dev/set_env_context.sh

# Provision the envtest binaries (etcd + kube-apiserver) used by envtest-based Go tests
# (see test/envtest) and expose them to `go test` via KUBEBUILDER_ASSETS.
make envtest-assets
KUBEBUILDER_ASSETS="$(make -s envtest-assets-path)"
export KUBEBUILDER_ASSETS

USE_RACE_SWITCH=""
if [[ "${USE_RACE:-"false"}" = "true" ]]; then
  USE_RACE_SWITCH="-race"
fi

USE_COVERAGE_SWITCH=""
if [[ "${USE_COVERAGE:-"false"}" = "true" ]]; then
  USE_COVERAGE_SWITCH="-coverprofile cover.out"
fi

rm golang-unit-result.xml cover.out 2>/dev/null || true

# gotestsum is just a wrapper on go test -json adding output formatting and xml file
# shellcheck disable=SC2086
go tool gotestsum --junitfile golang-unit-result.xml --format-icons hivis -- -v ${USE_RACE_SWITCH} ${USE_COVERAGE_SWITCH} ./...
