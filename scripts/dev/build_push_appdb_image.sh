#!/usr/bin/env bash
set -Eeou pipefail

cd "$(git rev-parse --show-toplevel || echo "Failed to find git root"; exit 1)"

source scripts/dev/set_env_context
source scripts/funcs/checks
source scripts/funcs/errors
source scripts/funcs/kubernetes
source scripts/funcs/printing

docker_build() {
    prepare_appdb_build
    ../Dockerfile.py appdb "${IMAGE_TYPE}" > Dockerfile

    docker build \
        -t "${repo_name}" \
        --build-arg MDB_URL="${mdb_url}" \
        --build-arg BINARY_NAME="${binary_name}" \
        --build-arg AA_DOWNLOAD_LOCATION="${agent_download_location}" \
        --build-arg AA_VERSION="${agent_version}" .
}

docker_push() {
    repo_name="$(echo "${full_url}" | cut -d "/" -f2-)" # cutting the domain part
    docker_build

    docker tag "${repo_name}" "${full_url}"
    docker push "${full_url}"
}
submit_kaniko() {
    # note that we disable caching for appdb images as both 0.16.0 and 0.17.1 Kaniko result in the following errors:
    # error building image: error building stage: failed to execute command: extracting fs from image: removing whiteout .wh.dev: unlinkat //dev/pts/ptmx: operation not permitted
    destination=${full_url} \
        context=s3://operator-kubernetes-build/ops-manager-operator/${version_id:?}/${IMAGE_TYPE}/contexts/appdb-context.tar.gz \
        cache_repo=268558157000.dkr.ecr.us-east-1.amazonaws.com/cache/mongodb-enterprise-appdb \
        label=appdb-${IMAGE_TYPE}-${version_id} \
        build_args="MDB_URL=${mdb_url},BINARY_NAME=${binary_name},AA_DOWNLOAD_LOCATION=${agent_download_location},AA_VERSION=${agent_version}" \
        ../../scripts/evergreen/build_docker_image.sh
}

# 1. First we need to collect all the parameters necessary for image build:
# agent_download_url, agent_version, bundled_mdb_url, bundled_mdb_binary_name

title "Building AppDB image..."

# om_branch is optional - only if the Ops Manager is not released yet and we need to use some branch
# if omitted it points to the relevant "on-prem-" tag
agent_download_location="${agent_download_location:-https://s3.amazonaws.com/mciuploads/mms-automation/mongodb-mms-build-agent/builds/automation-agent/prod}"

# download OM configuration file conf-hosted.properties to find the automation agent version if the version not passed directly
if [[ -z "${agent_version-}" ]]; then
    check_env_var "GITHUB_TOKEN" "To download Ops Manager config file you need to specify the 'GITHUB_TOKEN' environment variable (https://github.com/settings/tokens/new) with 'repo' scope"

    om_branch="on-prem-4.2.11"
    echo "Downloading mms conf-hosted file from the following tag/branch: $om_branch"
    curl -OL "https://${GITHUB_TOKEN}@raw.githubusercontent.com/10gen/mms/$om_branch/server/conf/conf-hosted.properties" --fail

    agent_version=$(grep "automation.agent.version" conf-hosted.properties | cut -d "=" -f 2)

    rm conf-hosted.properties

    if [[ -z ${agent_version} ]]; then
        fatal "Failed to find automation agent binary name for ops manager branch ${om_branch}"
    fi
fi

# note that in non local runs we want to build images with suffix (operator version).
if [[ "${LOCAL_RUN-}" = false ]]; then
    image_suffix="-operator$(git describe --dirty)"
    echo "Using image suffix (operator version) ${image_suffix} for building images"
fi

mdb_url=$(jq --raw-output .appDbBundle.baseUrl < release.json)
binary_name=$(jq --raw-output ".appDbBundle.${IMAGE_TYPE}BinaryName" < release.json)

echo "Data used for building the image: AA_location=${agent_download_location}, AA_version=${agent_version}, bundled_mdb_url=${mdb_url}, binary_name=${binary_name}"

# 2. Building the image - either to local repo or to the remote one (ECR)

cd docker/mongodb-enterprise-database || exit

if [[ ${REPO_TYPE} = "ecr" ]]; then
    ensure_ecr_repository "${REPO_URL}/mongodb-enterprise-appdb"
fi

# FIXME: remove om_version after moving to init appdb
for om_version in 4.2.0 4.2.3 4.2.4 4.2.6 4.2.7 4.2.8 4.2.10 4.2.11
do
    full_url="${REPO_URL}/mongodb-enterprise-appdb:${om_version}${image_suffix-}"
    sleep 10
    if [[ -n ${kaniko-} ]]; then
        # Note, that before submitting kaniko build the context needs to be uploaded to S3 - this is outside
        # of the scope for current script
        submit_kaniko
        title "Kaniko job for building AppDB image successfully created"
    else
        docker_push
        title "Database image successfully built and pushed to ${REPO_TYPE} registry"
    fi
done
