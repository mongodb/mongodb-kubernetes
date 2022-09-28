#!/usr/bin/env bash

set -xeou pipefail

# Here we rely on https://github.com/google/go-licenses/pull/152
# At the time of implementing this, it hasn't been tagged yet.
# In a few months, it's safe to switch to the `latest`
go install github.com/google/go-licenses@master

SCRIPTS_DIR=$(dirname $(readlink -f $0))

go-licenses report . --template "$SCRIPTS_DIR/update_licenses.tpl" > licenses_full.csv 2> licenses_stderr
cat licenses_full.csv | grep -v 10gen | grep -v "github.com/mongodb" | grep -v "golang.org"  | sort > licenses.csv
