#!/usr/bin/env bash
set -Eeou pipefail

export GOPATH="${workdir:?}"

go env

if ! [[ -x "$(command -v staticcheck)" ]]; then
    echo "installing gotools..."
    GOFLAGS="" go get -u honnef.co/go/tools/...
  else
    echo "go tools are already installed"
fi

# check for dead code
PATH=$GOPATH/bin:$PATH staticcheck -checks U1000,SA4006,ST1019,S1005,S1019 ./controllers/...

# some directories are excluded from vetting as they are auto-generated
vet_exclusions="github.com/10gen/ops-manager-kubernetes/pkg/client/clientset/versioned"

echo "Go Version: $(go version)"

# ensure there are no warnings detected with go vet
for package in $(go list ./... | grep -Fv "${vet_exclusions}")
do
    go vet "${package}"
done
