#!/usr/bin/env bash

# INFO: this is a trick to evaluate the contents of an environment variable.
versions=$(eval "${VERSIONS}")

prerelease_from=$(eval "${PRERELEASE_FROM}")
prerelease_to=$(eval "${PRERELEASE_TO}")

# We login into the destination registry because for now it is the Redhat Connect one
# The source registry is expected to be configured with a login manager (like ECR) or
# to be of local images.
docker login "${DOCKER_REGISTRY}" -u "${DOCKER_LOGIN_USER}" -p "${DOCKER_LOGIN_PASSWORD}"

for version in $versions; do
    from="${IMAGE_SOURCE}:${version}-${prerelease_from}"
    to="${IMAGE_TARGET}:${version}-${prerelease_to}"

    docker pull "${from}"
    docker tag "${from}" "${to}"
    docker push "${to}"
done
