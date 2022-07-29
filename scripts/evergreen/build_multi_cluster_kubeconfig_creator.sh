#!/usr/bin/env bash
set -Eeou pipefail

echo "Building multi cluster kube config creation tool."
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -buildvcs=false -o docker/mongodb-enterprise-tests/multi-cluster-kube-config-creator public/tools/multicluster/main.go
chmod +x docker/mongodb-enterprise-tests/multi-cluster-kube-config-creator
