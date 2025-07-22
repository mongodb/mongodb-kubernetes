#!/usr/bin/env bash

set -Eeou pipefail

source scripts/dev/set_env_context.sh

USE_RACE_SWITCH=""
if [[ "${USE_RACE:-"false"}" = "true" ]]; then
  USE_RACE_SWITCH="-race"
fi

rm golang-unit-result.xml cover.out 2>/dev/null || true

# gotestsum is just a wrapper on go test -json adding output formatting and xml file
go tool gotestsum --junitfile golang-unit-result.xml --format-icons hivis -- -v ${USE_RACE_SWITCH} -coverprofile cover.out ./...
