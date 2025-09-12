#!/usr/bin/env bash

set -Eeou pipefail

# Utility used to retag and push container images from one repo to another
# Useful for migrating images between different repositories (e.g. dev -> staging)

image_exists() {
    local image_tag="$1"
    if docker manifest inspect "${image_tag}" > /dev/null 2>&1; then
        return 0  # Image exists
    else
        return 1  # Image doesn't exist
    fi
}

push_new_tag() {
    local old_tag="$1"
    local new_tag="$2"
    echo "Retagging and pushing image ${old_tag} to ${new_tag}"

    docker pull "${old_tag}"
    docker tag "${old_tag}" "${new_tag}"
    docker push "${new_tag}"

    return 0
}

retag_container_image() {
    local image_name="$1"
    local version="$2"
    local old_repo="$3"
    local new_repo="$4"

    local old_tag="${old_repo}/${image_name}:${version}"
    local new_tag="${new_repo}/${image_name}:${version}"

    if image_exists "${new_tag}"; then
        echo "Image ${new_tag} exists, skipping"
    else
        push_new_tag "${old_tag}" "${new_tag}"
    fi

    return 0
}

versions=$(jq -r '.supportedImages."ops-manager".versions[]' release.json)

# Iterate over each version
for version in ${versions}; do
  echo "Retagging ops-manager version: ${version}"
  retag_container_image "mongodb-enterprise-ops-manager-ubi" "${version}" "268558157000.dkr.ecr.us-east-1.amazonaws.com/dev" "268558157000.dkr.ecr.us-east-1.amazonaws.com/staging"
done
