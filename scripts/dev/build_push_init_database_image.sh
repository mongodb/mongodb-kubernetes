#!/usr/bin/env bash
set -Eeou pipefail


source scripts/dev/set_env_context.sh
source scripts/funcs/kubernetes

# Building the init image for Database - either to local repo or to the remote one (ECR)

if [[ $(scripts/evergreen/guard_git_repo_is_clean) ]]
then
    # releasing the init database image
    init_database_version=$(jq --raw-output '.initDatabaseVersion' < release.json)
    build_id=$(date +%Y%m%d%H%M)
    suffix="-b${build_id}"
else
    # evg or local build
    init_database_version="${version_id:-latest}"
    suffix=""
fi

title "Building Init Database image (init database version: ${init_database_version})..."

repository_name="mongodb-enterprise-init-database"
repository_url="${INIT_DATABASE_REGISTRY:?}/${repository_name}"
ensure_ecr_repository "${repository_url}"

versioned_image="${repository_url}:${init_database_version}"
versioned_image_with_build="${versioned_image}${suffix}"

(
    [[ "${IMAGE_TYPE}" = "ubi" ]] && base_image="ubi_minimal" || base_image="busybox"
    cd docker/mongodb-enterprise-init-database
    ../dockerfile_generator.py init_database "${base_image}" > Dockerfile
)

# needs to be launched from the root for docker to be able to copy the probe directory
docker build -t "${versioned_image}" -t "${versioned_image_with_build}" --build-arg VERSION="${init_database_version}" -f docker/mongodb-enterprise-init-database/Dockerfile .
docker push "${versioned_image}"
docker push "${versioned_image_with_build}"
title "Init Database image successfully built and pushed to ${repository_url} registry"
