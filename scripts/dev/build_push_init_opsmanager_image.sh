#!/usr/bin/env bash
set -Eeou pipefail

cd "$(git rev-parse --show-toplevel)"

source scripts/dev/set_env_context.sh
source scripts/funcs/kubernetes

# Building the init image - either to local repo or to the remote one (ECR)

if [[ $(scripts/evergreen/guard_git_repo_is_clean) ]]
then
    init_om_version=$(jq --raw-output '.initOpsManagerVersion' < release.json)
    build_id=$(date +%Y%m%d%H%M)
    suffix="-b${build_id}"
else
    # evg or local build
    init_om_version="${version_id:-latest}"
    suffix=""
fi

title "Building Init Ops Manager image (init om version: ${init_om_version})..."

repository_name="mongodb-enterprise-init-ops-manager"
repository_url="${INIT_OPS_MANAGER_REGISTRY:?}/${IMAGE_TYPE:?}/${repository_name}"
ensure_ecr_repository "${repository_url}"

versioned_image="${repository_url}:${init_om_version}"
versioned_image_with_build="${versioned_image}${suffix}"

(
    [[ "${CLUSTER_TYPE}" = "openshift" ]] && base_image="ubi_minimal" || base_image="busybox"
    cd docker/mongodb-enterprise-init-ops-manager
    ../dockerfile_generator.py init_ops_manager "${base_image}" > Dockerfile

    # The images are tagged at build time with:
    #
    # - {ecr_registry}/dev/[rhel|ubuntu]/mongodb-enterprise-init-ops-manager:1.0.0
    # - {ecr_registry}/dev/[rhel|ubuntu]/mongodb-enterprise-init-ops-manager:1.0.0-b202004271010
    #
    docker build -t "${versioned_image}" -t "${versioned_image_with_build}" --build-arg VERSION="${init_om_version}" .
    docker push "${versioned_image}"
    docker push "${versioned_image_with_build}"
    title "Init Ops Manager image successfully built and pushed to ${repository_url} registry"
)
