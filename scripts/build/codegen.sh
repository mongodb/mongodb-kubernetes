#!/bin/bash -e

set -euo pipefail

DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"
cd "vendor/k8s.io/code-generator"

ROOT_PACKAGE="github.com/10gen/ops-manager-kubernetes"

# overriding go modules and GOFLAGS, because code-generator does not support go modules
input_dirs="${ROOT_PACKAGE}/pkg/apis/mongodb.com/v1,${ROOT_PACKAGE}/pkg/apis/mongodb.com/v1/status"
GO111MODULE=off GOFLAGS="" bash ./generate-groups.sh client,lister,informer ${ROOT_PACKAGE}/pkg/client \
    ${ROOT_PACKAGE}/pkg/apis "mongodb.com:v1" --go-header-file "${DIR}/boilerplate.go.txt"

# note, that the ./generate-groups.sh script doesn't support nested packages for deepcopy - so calling the deepcopy-gen
# manually passing all nested packages explicitly. The types needing 'deepCopy' method must be annotated
echo "Generating deepcopy methods for ${input_dirs}"
GO111MODULE=off GOFLAGS="" "${GOPATH}/bin/deepcopy-gen" --input-dirs "${input_dirs}" -O zz_generated.deepcopy \
    --bounding-dirs "${ROOT_PACKAGE}/pkg/apis" --go-header-file "${DIR}/boilerplate.go.txt"

