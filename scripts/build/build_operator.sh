#!/usr/bin/env bash

set -Eeou pipefail


# --dirty to flag changes to your working tree
RELEASE_VERSION=$(git describe)

# for running Operator locally we don't specify version (as the Operator version affects the OM/AppDB image used when
# deploying OM resource)
[[ -n "${LOCAL_RUN-}" ]] && RELEASE_VERSION=""

export GOOS=linux
export GO111MODULE=on
export GOFLAGS="-mod=vendor"

mdb_version="$(jq --raw-output .appDbBundle.mongodbVersion < release.json)"
echo "Using MongoDB version ${mdb_version} for the AppDB"

ldflags=(
    "-X github.com/10gen/ops-manager-kubernetes/pkg/util.OperatorVersion=${RELEASE_VERSION}"
    "-X github.com/10gen/ops-manager-kubernetes/pkg/util.LogAutomationConfigDiff=${LOG_AUTOMATION_CONFIG_DIFF:-false}"
    "-X github.com/10gen/ops-manager-kubernetes/pkg/util.BundledAppDbMongoDBVersion=${mdb_version}"
)
# Local operator is always built with debugging symbols enabled.
echo "Building Operator"
go build -gcflags "all=-N -l" -ldflags="${ldflags[*]}" \
    -o docker/mongodb-enterprise-operator/content/mongodb-enterprise-operator

echo "Operator successfully built (version ${RELEASE_VERSION})"
