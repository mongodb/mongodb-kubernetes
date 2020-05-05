#!/usr/bin/env bash
set -Eeou pipefail


source scripts/dev/set_env_context.sh
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

repository_name="mongodb-enterprise-init-appdb"
repository_url="${INIT_APPDB_REGISTRY:?}/${IMAGE_TYPE:?}/${repository_name}"
ensure_ecr_repository "${repository_url}"

versioned_image="${repository_url}:${init_appdb_version}"
versioned_image_with_build="${versioned_image}${suffix}"

(
    [[ "${CLUSTER_TYPE}" = "openshift" ]] && base_image="ubi_minimal" || base_image="busybox"
    cd docker/mongodb-enterprise-init-appdb
    ../dockerfile_generator.py init_appdb "${base_image}" > Dockerfile
)

# needs to be launched from the root for docker to be able to copy the probe directory
docker build -t "${versioned_image}" -t "${versioned_image_with_build}" --build-arg VERSION="${init_appdb_version}" -f docker/mongodb-enterprise-init-appdb/Dockerfile .
docker push "${versioned_image}"
docker push "${versioned_image_with_build}"
title "Init AppDB image successfully built and pushed to ${repository_url} registry"
