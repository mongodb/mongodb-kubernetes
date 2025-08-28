#!/usr/bin/env bash
set -Eeou pipefail

source scripts/dev/set_env_context.sh

WORKDIR="${workdir}"
mkdir -p "${WORKDIR}/bin"

OS=${OS:-$(uname -s | tr '[:upper:]' '[:lower:]')}
ARCH=${ARCH:-$(uname -m | tr '[:upper:]' '[:lower:]')}
if [[ "${ARCH}" == "x86_64" ]]; then
  ARCH="amd64"
elif [[ "${ARCH}" == "aarch64" ]]; then
  ARCH="arm64"
fi

echo "Building multi cluster kube config creation tool."

project_dir="$(pwd)"
pushd cmd/kubectl-mongodb
go mod download

GOOS="linux" GOARCH="amd64" CGO_ENABLED=0 go build -buildvcs=false -o "${project_dir}/docker/mongodb-kubernetes-tests/multi-cluster-kube-config-creator_amd64" main.go &
GOOS="linux" GOARCH="s390x" CGO_ENABLED=0 go build -buildvcs=false -o "${project_dir}/docker/mongodb-kubernetes-tests/multi-cluster-kube-config-creator_s390x" main.go &
GOOS="linux" GOARCH="ppc64le" CGO_ENABLED=0 go build -buildvcs=false -o "${project_dir}/docker/mongodb-kubernetes-tests/multi-cluster-kube-config-creator_ppc64le" main.go &
GOOS="linux" GOARCH="arm64" CGO_ENABLED=0 go build -buildvcs=false -o "${project_dir}/docker/mongodb-kubernetes-tests/multi-cluster-kube-config-creator_arm64" main.go &
wait
popd

# these are used in the dockerfile
chmod +x docker/mongodb-kubernetes-tests/multi-cluster-kube-config-creator_amd64
chmod +x docker/mongodb-kubernetes-tests/multi-cluster-kube-config-creator_s390x
chmod +x docker/mongodb-kubernetes-tests/multi-cluster-kube-config-creator_ppc64le
chmod +x docker/mongodb-kubernetes-tests/multi-cluster-kube-config-creator_arm64

cp docker/mongodb-kubernetes-tests/multi-cluster-kube-config-creator_${ARCH} docker/mongodb-kubernetes-tests/multi-cluster-kube-config-creator || true

mkdir -p bin || true
cp docker/mongodb-kubernetes-tests/multi-cluster-kube-config-creator bin/kubectl-mongodb || true
cp bin/kubectl-mongodb "${WORKDIR}/bin/kubectl-mongodb" || true
