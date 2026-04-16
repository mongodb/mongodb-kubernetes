#!/usr/bin/env bash

set -Eeou pipefail

source scripts/dev/set_env_context.sh

go run golang.org/x/vuln/cmd/govulncheck@latest ./...
