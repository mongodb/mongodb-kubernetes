#!/usr/bin/env bash
set -xeuo pipefail

eval $(minikube docker-env)
CGO_ENABLED=0 GOOS=linux go build -i -o om-operator
docker build -t mongodb-enterprise-operator .
