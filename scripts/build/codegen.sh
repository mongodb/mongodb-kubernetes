#!/bin/bash -e

set -euo pipefail

DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"
cd "vendor/k8s.io/code-generator"

ROOT_PACKAGE="github.com/10gen/ops-manager-kubernetes"

# overriding go modules and GOFLAGS, because code-generator does not support go modules
GO111MODULE=off GOFLAGS="" bash ./generate-groups.sh all ${ROOT_PACKAGE}/pkg/client ${ROOT_PACKAGE}/pkg/apis "mongodb.com:v1" --go-header-file "${DIR}/boilerplate.go.txt"
