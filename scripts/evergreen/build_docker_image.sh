#!/usr/bin/env bash


# Only create ECR credentials (as a Secret object) when the passed parameters have changed from
# what is stored in the currently existing aws-secret Secret object.
ensure_construction_site () {
    if [ -z "$AWS_ACCESS_KEY_ID" ] || [ -z "$AWS_SECRET_ACCESS_KEY" ] || [ -z "${AWS_REGION}" ]; then
        echo "Must provide AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY and AWS_REGION env variables!"
        exit 1
    fi

    aws_credentials="
[default]
aws_access_key_id = $AWS_ACCESS_KEY_ID
aws_secret_access_key = $AWS_SECRET_ACCESS_KEY
region = $AWS_REGION
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

    image_random_name=$(tr -dc 'a-z0-9' < /dev/urandom | fold -w 32 | head -n 1)

    ensure_construction_site

    MANIFEST_VERSION="$(jq --raw-output .appDbBundle.mongodbVersion < release.json | cut  -d. -f-2)"

    cat <<EOF | kubectl create -f -
apiVersion: v1
kind: Pod
metadata:
  name: builder-pod-${image_random_name}
  namespace: construction-site
  labels:
    podbuilderid: ${label}
spec:
  containers:
  - name: kaniko
    image: gcr.io/kaniko-project/executor:v0.16.0
    args: ["--dockerfile=Dockerfile",
           "--build-arg=MANIFEST_VERSION=${MANIFEST_VERSION}",
           "--destination=${destination}",
           "--context=${context}",
           "--cache=true",
           "--cache-repo=${cache_repo}"]
    volumeMounts:
    - name: aws-secret
      mountPath: /root/.aws/
    - name: docker-config
      mountPath: /kaniko/.docker/
  restartPolicy: Never
  volumes:
  - name: aws-secret
    secret:
      secretName: aws-secret
  - name: docker-config
    configMap:
      name: docker-config
EOF
}

if [ -n "$destination" ] && [ -n "$context" ] && [ -n "$cache_repo" ] && [ -n "$label" ]; then
  build_image "${destination}" "${context}" "${cache_repo}" "${label}"
fi
