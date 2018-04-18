#!/usr/bin/env bash
set -xeuo pipefail

CGO_ENABLED=0 GOOS=linux go build -i -o om-operator
docker build -t om-operator:0.1 .
