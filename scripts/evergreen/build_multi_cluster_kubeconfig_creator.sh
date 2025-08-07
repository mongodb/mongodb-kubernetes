#!/usr/bin/env bash
set -Eeou pipefail

source scripts/dev/set_env_context.sh

WORKDIR="${workdir}"
mkdir -p "${WORKDIR}/bin"

OS=${OS:-$(uname -s | tr '[:upper:]' '[:lower:]')}
ARCH=${ARCH:-$(uname -m | tr '[:upper:]' '[:lower:]')}
if [[ "${ARCH}" == "x86_64" ]]; then
  ARCH="amd64"
fi

echo "Building multi cluster kube config creation tool."

project_dir="$(pwd)"
pushd cmd/kubectl-mongodb
GOOS="${OS}" GOARCH="${ARCH}" CGO_ENABLED=0 go build -buildvcs=false -o "${project_dir}/docker/mongodb-kubernetes-tests/multi-cluster-kube-config-creator" main.go
GOOS="linux" GOARCH="amd64" CGO_ENABLED=0 go build -buildvcs=false -o "${project_dir}/docker/mongodb-kubernetes-tests/multi-cluster-kube-config-creator_linux" main.go
popd
chmod +x docker/mongodb-kubernetes-tests/multi-cluster-kube-config-creator

# this one is used for the dockerfile to build the test image running on linux, this script might create 2 times
# the same binary, but on the average case it creates one for linux and one for darwin-arm
chmod +x docker/mongodb-kubernetes-tests/multi-cluster-kube-config-creator_linux

mkdir -p bin || true
cp docker/mongodb-kubernetes-tests/multi-cluster-kube-config-creator bin/kubectl-mongodb || true
cp bin/kubectl-mongodb "${WORKDIR}/bin/kubectl-mongodb" || true
