#!/usr/bin/env bash
set -Eeou pipefail

echo "Building multi cluster kube config creation tool."
CGO_ENABLED=0 GOOS=linux GOARCH=386  go build -o docker/mongodb-enterprise-tests/multi-cluster-kube-config-creator multi_cluster/tools/cmd/main.go
chmod +x docker/mongodb-enterprise-tests/multi-cluster-kube-config-creator
