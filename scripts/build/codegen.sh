#!/bin/bash -e

set -xeuo pipefail

DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"
pushd "${DIR}" &> /dev/null
cd "$(../gitroot)"

# Arbitrary gitsha, but need to pick one so that the builds are repeatable
CODE_GENERATOR_GITSHA="4c99649af8fee16989100e4cc3e6a75143530b28"

if [ ! -d "vendor/k8s.io/code-generator" ]; then
    git clone https://github.com/kubernetes/code-generator.git vendor/k8s.io/code-generator
fi

cd "vendor/k8s.io/code-generator" || exit 1

git clean -fdx
git fetch
git checkout "${CODE_GENERATOR_GITSHA}"

./generate-groups.sh all github.com/10gen/ops-manager-kubernetes/pkg/client github.com/10gen/ops-manager-kubernetes/pkg/apis "mongodb.com:v1"
