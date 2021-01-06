#!/usr/bin/env bash
set -Eeou pipefail
set -x


source scripts/dev/set_env_context.sh
source scripts/funcs/checks
source scripts/funcs/errors
source scripts/funcs/kubernetes
source scripts/funcs/printing

title "Building AppDB image..."

# 1. First we need to collect all the parameters necessary for image build:
# bundled_mdb_url, bundled_mdb_binary_name

mdb_url=$(jq --raw-output .appDbBundle.baseUrl < release.json)
binary_name=$(jq --raw-output ".appDbBundle.${IMAGE_TYPE}BinaryName" < release.json)

echo "Data used for building the image: bundled_mdb_url=${mdb_url}, binary_name=${binary_name}"

# 2. Building the image - either to local repo or to the remote one (ECR)

ensure_ecr_repository "${REPO_URL}/mongodb-enterprise-appdb"

# FIXME: fetch AA version from new API when CLOUDP-58178 is deployed
# final_aa_download_url="$(curl --silent --show-error --fail --retry 3 -I ${aa_download_url} | grep -sE '^location: ' | cut -d' ' -f2 | tr -d '\r')"
# aa_version="$(echo -n "${final_aa_download_url}" | cut -d'/' -f7 | sed -e 's/^mongodb-mms-automation-agent-//' -e 's/.linux_x86_64.tar.gz$//')"

mdb_version=$(jq --raw-output .appDbBundle.mongodbVersion < release.json)
aa_version=$(jq --raw-output .appDBImageAgentVersion < release.json)
aa_download_url="https://s3.amazonaws.com/mciuploads/mms-automation/mongodb-mms-build-agent/builds/automation-agent/prod/mongodb-mms-automation-agent-${aa_version}.linux_x86_64.tar.gz"
tag_name="${aa_version}_${mdb_version}"


base_url="${REPO_URL}/mongodb-enterprise-appdb"
repo_name="$(echo "${base_url}" | cut -d "/" -f2-)" # cutting the domain part


(
    # all this happens in the docker directory
    cd docker/mongodb-enterprise-database
    ../dockerfile_generator.py appdb "${IMAGE_TYPE}" > Dockerfile

    build_id="b$(date -u +%Y%m%d%H%M)"
    docker build \
        -t "${repo_name}" \
        -t "${base_url}:${tag_name}" \
        -t "${base_url}:${tag_name}-${build_id}" \
        --build-arg AA_VERSION="${aa_version}" \
        --build-arg AA_DOWNLOAD_URL="${aa_download_url}" \
        --build-arg MDB_URL="${mdb_url}" \
        --build-arg BINARY_NAME="${binary_name}" .

    for version in "${tag_name}" "${tag_name}-${build_id}"
    do
        docker push "${base_url}:${version}"
        title "AppDB image successfully built and pushed to ${base_url}:${version}"
    done
)
