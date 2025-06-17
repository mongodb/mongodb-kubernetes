#!/usr/bin/env bash
set -Eeou pipefail -o posix

source scripts/dev/set_env_context.sh

# Evaluates a string passed as argument.
function evaluate {
    val="${1}"
    if [ ${#val} -gt 3 ] && [ "${val:0:1}" = "\$" ]; then
        # Removes the first '$(' (dollar + open paren) and the
        # last ')' (closing paren).
        eval "${val:2:-1}"
        return
    fi

    echo "${val}"
}

# We login into the destination registry for Redhat Connect or quay.
# The source registry is expected to be configured with a login manager (like ECR) or
# to be of local images.
docker_registry="$(echo "${image_target:?}"| cut -d / -f1)"
docker login "${docker_registry}" -u "${docker_username:?}" -p "${docker_password:?}"

final_tag_source=$(evaluate "${tag_source:?}")
final_tag_dest=$(evaluate "${tag_dest:?}")

echo "final_tag_source: |${final_tag_source}|"
echo "final_tag_dest: |${final_tag_dest}|"

from="${image_source:?}:${final_tag_source}"
to="${image_target:?}:${final_tag_dest}"

docker pull "${from}"
docker tag "${from}" "${to}"
docker push "${to}"

docker image rmi "${from}"
