#!/usr/bin/env bash
#
# A script Evergreen will use to compile the Operator
#
# This should be executed from root of the evergreen build dir
#

set -Eeou pipefail
set -x

# ${WORKDIR} refers to the MCI working directory
GOPATH="${WORKDIR}"
export GOPATH

if [ ! -f "docker/mongodb-enterprise-operator/Dockerfile" ] && [ -n "${IMAGE_TYPE:-}" ]; then
    echo "Generating Dockerfile for ${IMAGE_TYPE}"
    ( cd docker/mongodb-enterprise-operator && ../dockerfile_generator.py operator "${IMAGE_TYPE}" > Dockerfile )
fi

./scripts/build/prepare_build_environment
# This skips the regeneration of code + download of dependencies if this step was
# taken care of by the prepare_build_environment script.
export BUILD_ENVIRONMENT_READY=true

if [ -z "${SKIP_TESTING:-}" ]; then
    ./scripts/build/test_operator "$*"
fi
./scripts/build/build_operator.sh "$*"
