#!/usr/bin/env bash
set -Eeou pipefail


source scripts/dev/set_env_context.sh
source scripts/funcs/printing

header "Preparing Kaniko contexts for OpsManager"

echo "REPO_URL: $REPO_URL"

build_id="b$(date -u +%Y%m%d%H%M)"

declare -a labels=()

base_url="${REPO_URL}/mongodb-enterprise-ops-manager"

# prepares the dockerfile for Ops Manager.
( cd docker/mongodb-enterprise-ops-manager && ../dockerfile_generator.py opsmanager "${IMAGE_TYPE}" > Dockerfile )

# upload kaniko context
docker_dir_name="mongodb-enterprise-ops-manager" context="om" scripts/evergreen/upload_kaniko_context

for om_version in ${VERSIONS}; do
    download_url=""
    if [[ "${om_version}" == *"|"* ]]; then
        # this is a non-released version that has a specific download url
        download_url="$(echo "$om_version" | cut -d "|" -f 2)"
        om_version="$(echo "$om_version" | cut -d "|" -f 1)"
    fi
    header "Building Ops Manager Image $om_version (download url: ${download_url}) with kaniko"
    full_url="${base_url}:${om_version}-${build_id}"
    label="ops-manager-${om_version}-${build_id}-${IMAGE_TYPE}-${version_id:?}"
    label="$label" full_url="$full_url" om_version="${om_version}" download_url=${download_url} \
            docker/mongodb-enterprise-ops-manager/build_push_opsmanager_image.sh
    labels+=("${label}")
done

header "Kaniko jobs submitted, waiting until they finish"
labels="${labels[*]}" scripts/evergreen/wait_docker_image.sh

header "Tagging images with stable name"
for om_version in ${VERSIONS}; do
    echo "Tagging OpsManager ${om_version}"
    if [[ "${om_version}" == *"|"* ]]; then
        om_version="$(echo "$om_version" | cut -d "|" -f 1)"
    fi
    full_url="${base_url}:${om_version}-${build_id}"
    # TODO: parallel pull of all images
    docker pull "$full_url"
    image_id=$(docker images --format "{{.ID}}" "${full_url}")
    stable_url="${base_url}:${om_version}"
    docker tag "${image_id}" "$stable_url"
    docker push "${stable_url}"
done
