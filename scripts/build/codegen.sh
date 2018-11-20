#!/bin/bash -e

set -xeuo pipefail

DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"
pushd "${DIR}" &> /dev/null
cd "$(../gitroot)/vendor/k8s.io/code-generator"

./generate-groups.sh all github.com/10gen/ops-manager-kubernetes/pkg/client github.com/10gen/ops-manager-kubernetes/pkg/apis "mongodb.com:v1"
