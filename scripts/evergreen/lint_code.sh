#!/usr/bin/env bash

set -o nounset
set -o errexit
set -o pipefail

export GOPATH="${WORKDIR}"

if ! [[ -x "$(command -v staticcheck)" ]]; then
    echo "installing gotools..."
    GOFLAGS="" go get -u honnef.co/go/tools/...
  else
    echo "go tools are already installed"
fi

# check for dead code
PATH=$GOPATH/bin:$PATH staticcheck -checks U1000,SA4006,ST1019,S1005,S1019 ./pkg/controller/...

# some directories are excluded from vetting as they are auto-generated
vet_exclusions="github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1
github.com/10gen/ops-manager-kubernetes/pkg/client/"

echo "Go Version: $(go version)"

# ensure there are no warnings detected with go vet
go vet $(go list ./... | grep -Fv "$vet_exclusions")
