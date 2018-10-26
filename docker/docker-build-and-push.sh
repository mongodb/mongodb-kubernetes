#!/bin/bash
set -o nounset
set -o errexit

readonly REPOS=$( find . -name 'mongodb-enterprise-*' -type 'd' -exec bash -c 'echo ${1#./};' _ {} \; )

echo "Building and pushing Docker images:"
echo "${REPOS}"
echo
echo "Note: This script is using the CLUSTER_NAME and AWS_IMAGE_REPO environment variables; if undefined, it will fail!"
echo "      If you want to push to the default ECR repository (dev), use \"\$( make push )\" in each image's directory!"
echo

echo "Building and pushing images to '${AWS_IMAGE_REPO}/${CLUSTER_NAME}/'"
echo

# Log into ECR repo
eval "$(aws ecr get-login --no-include-email --region us-east-1)"

# Ensure repositories exist
for img in ${REPOS}; do aws ecr create-repository --repository-name "${CLUSTER_NAME}/${img}" || true; done
echo

# Build each image, tag it, and push it
sleep 3
for img in ${REPOS}; do
    pushd "${img}" 2> /dev/null

    # Build the image
    make build
    
    # Push to custom repository
    version=$( make version )
    docker tag "${img}:${version}" "${AWS_IMAGE_REPO}/${CLUSTER_NAME}/${img}:${version}"
    docker push "${AWS_IMAGE_REPO}/${CLUSTER_NAME}/${img}:${version}"
    
    popd 2> /dev/null
done
echo
echo "Pushed images to '${AWS_IMAGE_REPO}/${CLUSTER_NAME}/'"
echo "${REPOS}"
echo
