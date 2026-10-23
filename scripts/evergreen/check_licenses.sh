#!/usr/bin/env bash
set -Eeou pipefail

GO_LICENSES_VERSION=f43a3a422a28cd9830a03d2199cdafe5477d7f5b
GO_BIN_DIR=$(go env GOBIN)
GO_BIN_DIR=${GO_BIN_DIR:-"$(go env GOPATH)/bin"}
go install "github.com/nammn/go-licenses/v2@${GO_LICENSES_VERSION}"

for dir in . cmd/kubectl-mongodb mongodb-community-operator/cmd/readiness; do
    (
        cd "${dir}" && \
        goroot=$(GOTOOLCHAIN=auto go env GOROOT) && \
        PATH="${GO_BIN_DIR}:${goroot}/bin:${PATH}" \
        GOTOOLCHAIN=local GOROOT="${goroot}" \
        "${GO_BIN_DIR}/go-licenses" check ./... --include_tests --disallowed_types restricted,reciprocal \
        --ignore github.com/mongodb --ignore github.com/10gen
    )
done
