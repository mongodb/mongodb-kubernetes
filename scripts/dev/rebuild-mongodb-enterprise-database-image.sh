#!/usr/bin/env bash
set -xeuo pipefail

DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"
pushd "${DIR}" &> /dev/null || exit 1
cd "$(../gitroot)" || exit 1

docker build --no-cache docker/mongodb-enterprise-database -t mongodb-enterprise-database -f docker/mongodb-enterprise-database/Dockerfile
