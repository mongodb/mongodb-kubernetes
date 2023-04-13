#!/usr/bin/env bash

set -Eeou pipefail

export GOPATH="${workdir:-}"

go install github.com/google/go-licenses@v1.6.0

SCRIPTS_DIR=$(dirname "$(readlink -f "$0")")

PATH=$GOPATH/bin:$PATH go-licenses report . --template "$SCRIPTS_DIR/update_licenses.tpl" > licenses_full.csv 2> licenses_stderr

# shellcheck disable=SC2002
cat licenses_full.csv | grep -v 10gen | grep -v "github.com/mongodb" | grep -v "^golang.org"  | sort > licenses.csv
