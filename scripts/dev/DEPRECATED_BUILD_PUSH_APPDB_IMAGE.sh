#!/usr/bin/env bash
set -Eeou pipefail

cd "$(git rev-parse --show-toplevel || echo "Failed to find git root"; exit 1)"

source scripts/dev/set_env_context
source scripts/funcs/checks
source scripts/funcs/errors
source scripts/funcs/kubernetes
source scripts/funcs/printing

# TODO: move after init container for appdb is done
# prepare_appdb_build builds readiness probe and generates a Dockerfile for AppDB
# The function is reused from different places so put into 'func'
# Must be run from docker/mongodb-enterprise-database
prepare_appdb_build() {
       GOOS='linux' go build -i -o readiness ../../probe || \
          (../../scripts/build/prepare_build_environment && GOOS='linux' go build -i -o readiness ../../probe)
       mv readiness content/readinessprobe

       ../dockerfile_generator.py appdb "${IMAGE_TYPE}" > Dockerfile
}

docker_build() {
    prepare_appdb_build
    ../dockerfile_generator.py appdb "${IMAGE_TYPE}" > Dockerfile

    docker build \
        -t "${repo_name}" \
        --build-arg MDB_URL="${mdb_url}" \
        --build-arg BINARY_NAME="${binary_name}" \
        --build-arg AA_DOWNLOAD_URL="${aa_download_url}" \
        --build-arg AA_VERSION="${aa_version}" .
}

docker_push() {
    repo_name="$(echo "${full_url}" | cut -d "/" -f2-)" # cutting the domain part
    docker_build

    docker tag "${repo_name}" "${full_url}"
    docker push "${full_url}"
}

# 1. First we need to collect all the parameters necessary for image build:
# agent_download_url, agent_version, bundled_mdb_url, bundled_mdb_binary_name

title "Building AppDB image..."

# ugly but this script is going away altogether
aa_version="10.2.15.5958-1"
aa_download_url="https://s3.amazonaws.com/mciuploads/mms-automation/mongodb-mms-build-agent/builds/automation-agent/prod/mongodb-mms-automation-agent-${aa_version}.linux_x86_64.tar.gz"

# note that in non local runs we want to build images with suffix (operator version).
if [[ "${LOCAL_RUN-}" = false ]]; then
    image_suffix="-operator$(git describe --dirty)"
    echo "Using image suffix (operator version) ${image_suffix} for building images"
fi

mdb_url=$(jq --raw-output .appDbBundle.baseUrl < release.json)
binary_name=$(jq --raw-output ".appDbBundle.${IMAGE_TYPE}BinaryName" < release.json)

echo "Data used for building the image: AA_location=${aa_download_url}, AA_version=${aa_version}, bundled_mdb_url=${mdb_url}, binary_name=${binary_name}"

# 2. Building the image - either to local repo or to the remote one (ECR)

cd docker/mongodb-enterprise-database || exit

if [[ ${REPO_TYPE} = "ecr" ]]; then
    ensure_ecr_repository "${REPO_URL}/mongodb-enterprise-appdb"
fi

# FIXME: remove om_version after moving to init appdb
for om_version in 4.2.0 4.2.3 4.2.4 4.2.6 4.2.7 4.2.8 4.2.10 4.2.11
do
    full_url="${REPO_URL}/mongodb-enterprise-appdb:${om_version}${image_suffix-}"

    docker_push
    sleep 10
    title "Database image successfully built and pushed to ${REPO_TYPE} registry"
done
