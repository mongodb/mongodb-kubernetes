#!/usr/bin/env bash

set -xeou pipefail

make
go-licenses csv . > licenses_full.csv 2> licenses_stderr
cat licenses_full.csv | grep -v 10gen | grep -v "github.com/mongodb"  | sort > licenses.csv
