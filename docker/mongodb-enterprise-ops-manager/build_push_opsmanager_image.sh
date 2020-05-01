#!/usr/bin/env bash
set -Eeou pipefail

cd "$(git rev-parse --show-toplevel)"

source scripts/dev/set_env_context.sh
source scripts/funcs/checks
source scripts/funcs/errors
source scripts/funcs/kubernetes
source scripts/funcs/printing


check_mandatory_param "${om_version-}" "om_version"
check_mandatory_param "${full_url-}" "full_url"
check_mandatory_param "${label-}" "label"

om_download_url() {
    check_mandatory_param "${1-}" "om_version"
    om_version=${1}
    # first search in current releases
    link=$(curl -l https://info-mongodb-com.s3.amazonaws.com/com-download-center/ops_manager_release_archive.json | \
              jq -r '.currentReleases[] | select(.version == "'"${om_version}"'") | .platform[] | select(.package_format=="deb") | select(.arch=="x86_64") | .packages.links[] | select(.name=="tar.gz") | .download_link')

    # searching in old releases
    [[ -z ${link} ]] && link=$(curl -l https://info-mongodb-com.s3.amazonaws.com/com-download-center/ops_manager_release_archive.json | \
              jq -r '.oldReleases[] | select(.version == "'"${om_version}"'") | .platform[] | select(.package_format=="deb") | select(.arch=="x86_64") | .packages.links[] | select(.name=="tar.gz") | .download_link')

    echo "${link}"
}


# 1. First we need to find out the download_url necessary for image build

title "Building Ops Manager image (om version: ${om_version})..."

download_url="$(om_download_url "$om_version")"

echo "The download url for ${om_version} is ${download_url}"


# 2. Building the image to the remote repo (ECR)

ensure_ecr_repository "${REPO_URL}/mongodb-enterprise-ops-manager"

# submit to kaniko
destination="${full_url:?}" \
    context="s3://operator-kubernetes-build/ops-manager-operator/${version_id:?}/${IMAGE_TYPE}/contexts/om-context.tar.gz" \
    cache_repo="${ecr_registry:?}/cache/mongodb-enterprise-ops-manager" \
    label="${label:?}" \
    build_args="OM_DOWNLOAD_LINK=${download_url}" \
    scripts/evergreen/build_docker_image.sh

title "Kaniko job for building Ops Manager image successfully created"
