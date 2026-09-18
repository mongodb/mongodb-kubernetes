#!/bin/bash

set -euo pipefail

# Branch scripts that predate the context split hardcode the Evergreen build
# host's Go layout (`export GOROOT=/opt/golang/go1.25`). Point that path at the
# toolchain the devcontainer actually ships so those scripts keep working on
# any branch, whatever the pinned Go version.
goroot="$(go env GOROOT)"
if [[ -d "${goroot}" && ! -e /opt/golang/go1.25 ]]; then
  sudo mkdir -p /opt/golang
  sudo ln -sfn "${goroot}" /opt/golang/go1.25
fi
