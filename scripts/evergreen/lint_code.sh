#!/usr/bin/env bash

set -o nounset
set -o errexit
set -o pipefail

GOPATH="${WORKDIR}"
export GOPATH

export GOROOT="/usr/lib/go"
export GOBIN="${GOPATH}/bin"
export PATH="${GOBIN}:${PATH}"

if ! [[ -x "$(command -v goimports)" ]]; then
    echo "installing goimports..."
    go get golang.org/x/tools/cmd/goimports
else
    echo "goimports is already installed"
fi

exit_code=0

# ensure all code has been formatted with goimports
if [[ "$($GOPATH/bin/goimports -l ./pkg/controller ./pkg/util ./pkg/apis main.go)" ]]; then
    echo "ERROR: Not all code has been formatted with goimports."
    echo "Run: goimports -w ./pkg/controller ./pkg/util ./pkg/apis main.go"
    exit_code=1
else
  echo "there were no goimports warnings!"
fi

# ensure there are no warnings detected with go vet
if ! go vet ./pkg/controller/... ./pkg/util/... ./pkg/apis/... || ! go vet main.go ; then
  echo "ERROR: Failed go vet check"
  echo "Run: go vet ./pkg/controller/... ./pkg/util/... ./pkg/apis/... && go vet main.go"
  exit_code=1
else
  echo "there were no go vet warnings!"
fi

exit ${exit_code}