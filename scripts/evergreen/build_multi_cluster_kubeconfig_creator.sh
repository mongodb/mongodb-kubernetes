#!/usr/bin/env bash
set -Eeou pipefail

OS=${OS:-$(uname -s | tr '[:upper:]' '[:lower:]')}
ARCH=${ARCH:-$(uname -m | tr '[:upper:]' '[:lower:]')}
if [[ "${ARCH}" == "x86_64" ]]; then
  ARCH="amd64"
fi

echo "Building multi cluster kube config creation tool."

project_dir="$(pwd)"
pushd public/tools/multicluster
GOOS="${OS}" GOARCH="${ARCH}" go build -buildvcs=false -o "${project_dir}/docker/mongodb-enterprise-tests/multi-cluster-kube-config-creator" main.go
popd
chmod +x docker/mongodb-enterprise-tests/multi-cluster-kube-config-creator

mkdir -p bin || true
cp docker/mongodb-enterprise-tests/multi-cluster-kube-config-creator bin/kubectl-mongodb

