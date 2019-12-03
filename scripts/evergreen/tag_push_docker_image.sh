#!/usr/bin/env bash

# Joins n strings using the first character passed as parameter.
function join { local IFS="$1"; shift; echo "$*"; }

# Evaluates a string passed as argument.
function evaluate {
    val="${1}"
    if [ ${#val} -gt 3 ] && [ "${val:0:1}" = "\$" ]; then
        # Removes the first '$(' (dollar + open paren) and the
        # last ')' (closing paren).
        echo $(eval "${val:2:-1}")
        return
    fi

    echo "${val}"
}

# Simple function to pull, tag and push an image.
function docker_pull_tag_push {
    from=${1}
    to=${2}

    docker pull "${from}"
    docker tag "${from}" "${to}"
    docker push "${to}"
}

# We login into the destination registry because for now it is the Redhat Connect one
# The source registry is expected to be configured with a login manager (like ECR) or
# to be of local images.
docker login "${DOCKER_REGISTRY}" -u "${DOCKER_LOGIN_USER}" -p "${DOCKER_LOGIN_PASSWORD}"

versions=$(evaluate "${VERSIONS}")
for version in $versions; do
    tag_source=$(evaluate "${TAG_SOURCE}")
    tag_dest=$(evaluate "${TAG_DEST}")

    echo "tag_source: |${tag_source}|"
    echo "tag_dest: |${tag_dest}|"

    from="${IMAGE_SOURCE}:${tag_source}"
    to="${IMAGE_TARGET}:${tag_dest}"

    docker_pull_tag_push "${from}" "${to}"
done
