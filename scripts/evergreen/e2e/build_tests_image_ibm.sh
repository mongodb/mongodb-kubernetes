#!/usr/bin/env bash
source scripts/dev/set_env_context.sh

# we need to use podman here as ibm machines don't have docker, therefore our current pipeline can't build these images

cp -rf public docker/mongodb-kubernetes-tests/public
cp release.json docker/mongodb-kubernetes-tests/release.json
cp requirements.txt docker/mongodb-kubernetes-tests/requirements.txt
cp -rf helm_chart docker/mongodb-kubernetes-tests/helm_chart

echo "Building mongodb-kubernetes-tests image with tag: ${REGISTRY}/mongodb-kubernetes-tests:${OPERATOR_VERSION}-$(arch)"
cd docker/mongodb-kubernetes-tests || exit
sudo podman buildx build --progress plain . -f Dockerfile -t "${REGISTRY}/mongodb-kubernetes-tests:${OPERATOR_VERSION}-$(arch)" --build-arg PYTHON_VERSION="${PYTHON_VERSION}"
sudo podman push --authfile="/root/.config/containers/auth.json" "${REGISTRY}/mongodb-kubernetes-tests:${OPERATOR_VERSION}-$(arch)"
