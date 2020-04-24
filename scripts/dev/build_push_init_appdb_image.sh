#!/usr/bin/env bash
set -Eeou pipefail

cd "$(git rev-parse --show-toplevel || echo "Failed to find git root"; exit 1)"

source scripts/dev/set_env_context
source scripts/funcs/kubernetes

# Building the init image - either to local repo or to the remote one (ECR)

if [[ $(scripts/evergreen/guard_git_repo_is_clean) ]]
then
    init_appdb_version=$(jq --raw-output '.initAppDbVersion' < release.json)
    build_id=$(date +%Y%m%d%H%M)
    suffix="-b${build_id}"
else
    # evg or local build
    init_appdb_version="${version_id:-latest}"
    suffix=""
fi

title "Building Init AppDB image (init appdb version: ${init_appdb_version})..."

pushd "${PWD}"

if [[ ${REPO_TYPE} = "ecr" ]]; then
    ensure_ecr_repository "${INIT_APPDB_REGISTRY:?}/mongodb-enterprise-init-appdb"
fi

base_url="${INIT_APPDB_REGISTRY}/mongodb-enterprise-init-appdb"
version="${init_appdb_version}${suffix}"
full_url="${base_url}:${version}"
# needs to be launched from the root for docker to be able to copy the probe directory
docker build -t "${base_url}" -t "${full_url}" --build-arg VERSION="${init_appdb_version}" -f docker/mongodb-enterprise-init-appdb/Dockerfile .
docker push "${full_url}"
title "Init AppDB image successfully built and pushed to ${INIT_APPDB_REGISTRY} registry"
