#!/usr/bin/env bash
set -Eeou pipefail


source scripts/dev/set_env_context.sh
source scripts/funcs/printing
source scripts/funcs/kubernetes


# Building the database image. No extra compilations for probes etc are necessary as the database
# image is empty and all other files (scripts, probes) will be added in runtime by database-init container
# Must be called from the root of the project

title "Building Database image (version: ${database_version:?})..."

if [[ ${REPO_TYPE} = "ecr" ]]; then
    ensure_ecr_repository "${REPO_URL}/mongodb-enterprise-database"
fi

base_url="${REPO_URL}/mongodb-enterprise-database"
repo_name="$(echo "${base_url}" | cut -d "/" -f2-)" # cutting the domain part


(
    # all this happens in the docker directory
    cd docker/mongodb-enterprise-database
    ../dockerfile_generator.py database "${IMAGE_TYPE}" > Dockerfile

    build_id="b$(date -u +%Y%m%d%H%M)"
    docker build \
        -t "${repo_name}" \
        -t "${base_url}:${database_version}" \
        -t "${base_url}:${database_version}-${build_id}" \
        -t "${base_url}:latest" \
        --build-arg version="${database_version}" .

    for version in "${database_version}" "${database_version}-${build_id}" "latest"
    do
        docker push "${base_url}:${version}"
    done
)

title "Database image successfully built and pushed to ${REPO_URL} registry"
