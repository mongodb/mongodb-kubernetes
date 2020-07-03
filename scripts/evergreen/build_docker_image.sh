#!/usr/bin/env bash
set -Eeou pipefail


# Only create ECR credentials (as a Secret object) when the passed parameters have changed from
# what is stored in the currently existing aws-secret Secret object.
ensure_construction_site () {
    if [ -z "$AWS_ACCESS_KEY_ID" ] || [ -z "$AWS_SECRET_ACCESS_KEY" ]; then
        echo "Must provide AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY env variables!"
        exit 1
    fi

    aws_credentials="
[default]
aws_access_key_id = ${AWS_ACCESS_KEY_ID}
aws_secret_access_key = ${AWS_SECRET_ACCESS_KEY}
region = ${AWS_REGION:-us-east-1}
"
    echo "ensuring construction-site namespace"
    kubectl create namespace construction-site --dry-run -o yaml | kubectl apply -f -

    echo "ensuring default service account"
    kubectl create serviceaccount default -n construction-site --dry-run -o yaml | kubectl apply -f -

    echo "ensuring aws secret"
    kubectl -n construction-site create secret generic aws-secret --from-literal=credentials="$aws_credentials" --dry-run -o yaml | kubectl apply -f -

    echo "ensuring docker-config"
    kubectl -n construction-site create configmap docker-config --from-literal=config.json='{"credHelpers":{"268558157000.dkr.ecr.us-east-1.amazonaws.com":"ecr-login"}}' --dry-run -o yaml | kubectl apply -f -
}

split_version_into_sha() {
    # split first paramter by _ delimiter and return 20 chars from the last part
    version="${1}"

    IFS="_" read -ra parts <<< "$version"

    echo "${parts[-1]:0:20}"
}

build_image () {
    destination="${1}"
    context="${2}"
    cache_repo="${3}"
    label="${4:0:63}"  # make sure label is not longer than 63 chars

    image_random_name="$RANDOM"

    ensure_construction_site

    if [[ "${context}" == *"operator-context"* ]]; then
        # it's not possible to call shell from evg when setting env variables, let's do it here
        build_args=MANIFEST_VERSION="$(jq --raw-output .appDbBundle.mongodbVersion < release.json | cut  -d. -f-2)"
    fi

    tmp_file=$(mktemp)
    helm template "scripts/evergreen/deployments/kaniko" \
     --set podName="${image_random_name}" \
     --set label="${label}" \
     --set buildArgs="{${build_args-}}" \
     --set destination="${destination}" \
     --set context="${context}" \
     --set cache="${cache:-true}" \
     --set cacheRepo="${cache_repo}" > "${tmp_file}"

     cat "${tmp_file}"

     kubectl -n "construction-site" apply -f "${tmp_file}"

     rm "${tmp_file}"
}

build_image "${destination}" "${context}" "${cache_repo}" "${label}"
