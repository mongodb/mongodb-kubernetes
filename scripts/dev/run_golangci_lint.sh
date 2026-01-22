#!/usr/bin/env bash

set -Eeou pipefail

# Enable synctest experiment for testing/synctest package support
export GOEXPERIMENT=synctest

golangci-lint run --new-from-rev HEAD --fix --timeout=10m
