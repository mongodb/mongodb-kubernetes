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

aa_download_url="https://cloud.mongodb.com/download/agent/automation/mongodb-mms-automation-agent-latest.linux_x86_64.tar.gz"

echo "Data used for building the image: bundled_mdb_url=${mdb_url}, binary_name=${binary_name}"

# 2. Building the image - either to local repo or to the remote one (ECR)

if [[ ${REPO_TYPE} = "ecr" ]]; then
    ensure_ecr_repository "${REPO_URL}/mongodb-enterprise-appdb"
fi

# FIXME: fetch AA version from new API when CLOUDP-58178 is deployed
# final_aa_download_url="$(curl --silent --show-error --fail --retry 3 -I ${aa_download_url} | grep -sE '^location: ' | cut -d' ' -f2 | tr -d '\r')"
# aa_version="$(echo -n "${final_aa_download_url}" | cut -d'/' -f7 | sed -e 's/^mongodb-mms-automation-agent-//' -e 's/.linux_x86_64.tar.gz$//')"

# ugly but latest automation agent requires mongoDbTools field that we currently don't pass and is not supported in OpsManager 4.2 anyways
aa_version="10.2.15.5958-1"
aa_download_url="https://s3.amazonaws.com/mciuploads/mms-automation/mongodb-mms-build-agent/builds/automation-agent/prod/mongodb-mms-automation-agent-${aa_version}.linux_x86_64.tar.gz"



base_url="${REPO_URL}/mongodb-enterprise-appdb"
repo_name="$(echo "${base_url}" | cut -d "/" -f2-)" # cutting the domain part


(
    # all this happens in the docker directory
    cd docker/mongodb-enterprise-database
    ../dockerfile_generator.py appdb "${IMAGE_TYPE}" > Dockerfile

    build_id="b$(date -u +%Y%m%d%H%M)"
    docker build \
        -t "${repo_name}" \
        -t "${base_url}:${aa_version}" \
        -t "${base_url}:${aa_version}-${build_id}" \
        -t "${base_url}:latest" \
        --build-arg AA_VERSION="${aa_version}" \
        --build-arg AA_DOWNLOAD_URL="${aa_download_url}" \
        --build-arg MDB_URL="${mdb_url}" \
        --build-arg BINARY_NAME="${binary_name}" .

    for version in "${aa_version}" "${aa_version}-${build_id}" "latest"
    do
        docker push "${base_url}:${version}"
    done
)

title "AppDB image successfully built and pushed to ${REPO_URL} registry"
