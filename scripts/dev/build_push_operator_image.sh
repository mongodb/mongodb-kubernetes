#!/usr/bin/env bash
set -Eeou pipefail

cd "$(git rev-parse --show-toplevel)"

source scripts/dev/set_env_context.sh
source scripts/funcs/kubernetes
source scripts/funcs/printing

build_and_push() {
    pushd docker/mongodb-enterprise-operator 2> /dev/null

    local debug_flag=${DEBUG:+"--debug"}
    local manifest_version
    local repo_name
    manifest_version=$(jq --raw-output .appDbBundle.mongodbVersion < ../../release.json | cut  -d. -f-2)
    repo_name="$(echo "${full_url}" | cut -d "/" -f2-)" # cutting the domain part

    # (debug_flag doesn't work with double quotes)
    # shellcheck disable=SC2086
    ../dockerfile_generator.py operator "${IMAGE_TYPE}" ${debug_flag} > Dockerfile
    ../../scripts/build/build_operator || (../../scripts/build/prepare_build_environment && ../../scripts/build/build_operator)
	docker build --build-arg MANIFEST_VERSION="${manifest_version}" -t "${repo_name}" .
	docker tag "${repo_name}" "${full_url}"
	docker push "${full_url}"

	popd 2> /dev/null
}
export DEBUG="${1-}"

title "Building Operator image... (debug: ${DEBUG:-'false'})"

full_url="${REPO_URL}/mongodb-enterprise-operator"
if [[ ${REPO_TYPE} = "ecr" ]] ; then
    ensure_ecr_repository "$full_url"

    if [[ -n "${DEBUG-}" ]]; then
        echo "Ensuring Security Group vpc for debug-svc"
        prefix="$(echo "nodes.${CLUSTER_NAME}" | sed s/.mongokubernetes.com//)"
        # extract the aws region from the kops output
        region="$(kops get cluster "${CLUSTER_NAME}" | awk '{ print $3 }' | tail -1 | cut -d ',' -f1)"
        region="${region%?}" # strip out the last "a", "b" or "c"
        group_id=$(aws ec2 describe-security-groups --region "${region}" | jq -r '.SecurityGroups[] | select(.GroupName | startswith( "'"$prefix"'")) | .GroupId')
        aws ec2 authorize-security-group-ingress --region "${region}" --group-id "${group_id}" --protocol tcp --port 30042 --cidr "0.0.0.0/0" 2>/dev/null || true
        echo "Building Operator Image in 'debug' mode"
    fi
fi

build_and_push

title "Operator image successfully built and pushed to the ${REPO_TYPE} registry"
