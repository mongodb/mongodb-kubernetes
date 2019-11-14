#!/usr/bin/env bash


# Only create ECR credentials (as a Secret object) when the passed parameters have changed from
# what is stored in the currently existing aws-secret Secret object.
ensure_ecr_credentials () {
    if [ -z "$AWS_ACCESS_KEY_ID" ] || [ -z "$AWS_SECRET_ACCESS_KEY" ] || [ -z "${AWS_REGION}" ]; then
        return
    fi

    old_credentials=$(kubectl -n construction-site get secret/aws-secret -o jsonpath='{.data.credentials}' | base64 --decode)
    old_access_key_id=$(echo "$old_credentials" | grep "aws_access_key_id"| awk '{print $3}')
    old_secret_access_key=$(echo "$old_credentials" | grep "aws_secret_access_key" | awk '{print $3}')

    if [[ $old_access_key_id != "$AWS_ACCESS_KEY_ID" ]] || [[ $old_secret_access_key != "$AWS_SECRET_ACCESS_KEY" ]]; then
        echo "Credentials have been modified, regenarating secrets/aws-secret"
    else
        return
    fi

    aws_credentials="
[default]
aws_access_key_id = $AWS_ACCESS_KEY_ID
aws_secret_access_key = $AWS_SECRET_ACCESS_KEY
region = $AWS_REGION
"

    kubectl -n construction-site delete secret/aws-secret
    kubectl -n construction-site create secret generic aws-secret --from-literal=credentials="$aws_credentials" &> /dev/null || true
}

split_version_into_sha() {
    # split first paramter by _ delimiter and return 20 chars from the last part
    version="${1}"

    IFS="_" read -ra parts <<< "$version"

    echo "${parts[-1]:0:20}"
}

build_image () {
    destination="${1}"
    tag="${2}"
    context="${3}"
    build_arg="${4}"
    cache_repo="${5}"
    pod_builder_id="${6}"

    image_random_name=$(tr -dc 'a-z0-9' < /dev/urandom | fold -w 8 | head -n 1)

    ensure_ecr_credentials

    cat <<EOF | kubectl create -f -
apiVersion: v1
kind: Pod
metadata:
  name: ${pod_builder_id}
  namespace: construction-site
  labels:
    podbuilderid: ${pod_builder_id}
spec:
  containers:
  - name: kaniko
    image: gcr.io/kaniko-project/executor:latest
    args: ["--dockerfile=Dockerfile",
           "--destination=${destination}:${tag}",
           "--build-arg=${build_arg}",
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

if [ $# -eq 6 ]; then
    build_image $@
fi
