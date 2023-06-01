#!/usr/bin/env bash
set -Eeou pipefail

echo "Building multi cluster kube config creation tool."
pushd public/tools/multicluster
GOOS=linux GOARCH=amd64 go build -buildvcs=false -o ../../../docker/mongodb-enterprise-tests/multi-cluster-kube-config-creator main.go
popd
chmod +x docker/mongodb-enterprise-tests/multi-cluster-kube-config-creator
