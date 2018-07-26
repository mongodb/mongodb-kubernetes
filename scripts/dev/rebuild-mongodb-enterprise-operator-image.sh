#!/usr/bin/env bash
set -xeuo pipefail

DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"
pushd "${DIR}" &> /dev/null || exit 1
cd "$(../gitroot)" || exit 1

./scripts/build/build_operator
docker build --no-cache docker/mongodb-enterprise-operator -t mongodb-enterprise-operator -f docker/mongodb-enterprise-operator/Dockerfile
