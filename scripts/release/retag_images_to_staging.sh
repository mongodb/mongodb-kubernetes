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

get_arch_digest() {
    local image="$1"
    local arch="$2"
    local manifest_json
    manifest_json=$(docker buildx imagetools inspect --raw "${image}" 2>/dev/null || echo '{}')

    local media_type
    media_type=$(echo "${manifest_json}" | jq -r '.mediaType // empty')

    if [[ "${media_type}" == *"manifest.list"* ]]; then
        # this is a multi-arch manifest
        local arch_digest
        arch_digest=$(echo "${manifest_json}" | jq -r ".manifests[] | select(.platform.architecture == \"${arch}\" and .platform.os == \"linux\") | .digest")

        if [[ -n "${arch_digest}" && "${arch_digest}" != "null" ]]; then
            echo "${arch_digest}"
            return 0
        fi
    elif [[ "${arch}" == "amd64" ]]; then
      # otherwise it must be a single-arch (image) manifest, so we return it only if we ask for amd64
      local arch_digest
      arch_digest="sha256:$(echo -n "${manifest_json}" | shasum --algorithm 256 | head -c 64)"
      echo "${arch_digest}"
    fi

    echo ""
    return 0
}

process_image() {
    local source_image="$1"
    local target_image="$2"

    echo "  Processing ${source_image}..."

    local digest_arm64
    local digest_amd64
    digest_arm64=$(get_arch_digest "${source_image}" arm64)
    digest_amd64=$(get_arch_digest "${source_image}" amd64)

    if [[ -n "${digest_amd64}" ]]; then
      docker pull "${source_image}@${digest_amd64}"
      docker tag "${source_image}@${digest_amd64}" "${target_image}-amd64"
      docker push "${target_image}-amd64"
    fi

    if [[ -n "${digest_arm64}" ]]; then
      docker pull "${source_image}@${digest_arm64}"
      docker tag "${source_image}@${digest_arm64}" "${target_image}-arm64"
      docker push "${target_image}-arm64"
    fi

    docker manifest create "${target_image}" ${digest_amd64:+--amend ${target_image}-amd64} ${digest_arm64:+--amend ${target_image}-arm64}
    docker manifest push "${target_image}"
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
        process_image "${old_tag}" "${new_tag}"
    fi

    return 0
}

versions=$(jq -r '.supportedImages."ops-manager".versions[]' release.json)

# Iterate over each version
for version in ${versions}; do
  echo "Retagging ops-manager version: ${version}"
  retag_container_image "mongodb-enterprise-ops-manager-ubi" "${version}" "268558157000.dkr.ecr.us-east-1.amazonaws.com/dev" "268558157000.dkr.ecr.us-east-1.amazonaws.com/staging"
done
