#!/usr/bin/env bash
set -Eeou pipefail

# This script prepares two openshift bundles tgz: certified and community.

RELEASE_JSON_PATH=${RELEASE_JSON_PATH:-"release.json"}
VERSION=${VERSION:-$(jq -r .mongodbOperator < "${RELEASE_JSON_PATH}")}
BUILD_DOCKER_IMAGES=${BUILD_DOCKER_IMAGES:-"false"}
OPERATOR_IMAGE=${OPERATOR_IMAGE:-"quay.io/mongodb/mongodb-enterprise-operator:${VERSION}"}
DOCKER_PLATFORM=${DOCKER_PLATFORM:-"linux/amd64"}

mkdir -p bundle

echo "Generating openshift bundle for version ${VERSION}"
make bundle VERSION="${VERSION}" IMG="${OPERATOR_IMAGE}"

mv bundle.Dockerfile "./bundle/${VERSION}/bundle.Dockerfile"

minimum_supported_openshift_version=$(jq -r .openshift.minimumSupportedVersion < "${RELEASE_JSON_PATH}")
bundle_annotations_file="bundle/${VERSION}/metadata/annotations.yaml"
bundle_dockerfile="bundle/${VERSION}/bundle.Dockerfile"

echo "Adding minimum_supported_openshift_version annotation to ${bundle_annotations_file}"
yq e ".annotations.\"com.redhat.openshift.versions\" = \"v${minimum_supported_openshift_version}\"" -i "${bundle_annotations_file}"

echo "Adding minimum_supported_openshift_version annotation to ${bundle_dockerfile}"
echo "LABEL com.redhat.openshift.versions=\"v${minimum_supported_openshift_version}\"" >> "${bundle_dockerfile}"

community_bundle_file="./bundle/operator-community-${VERSION}.tgz"
echo "Generating community bundle"
tar -czvf "${community_bundle_file}" "./bundle/${VERSION}"

if [[ "${BUILD_DOCKER_IMAGES}" == "true" ]]; then
  docker build --platform "${DOCKER_PLATFORM}" -f "./bundle/${VERSION}/bundle.Dockerfile" -t "${COMMUNITY_BUNDLE_IMAGE}" .
  docker push "${COMMUNITY_BUNDLE_IMAGE}"
fi

PATH="${PATH}:bin"

echo "Running digest pinning for certified bundle"
if [[ "${DIGEST_PINNING_ENABLED:-"true"}" == "true" ]]; then
  operator-manifest-tools pinning pin -v --resolver skopeo "bundle/${VERSION}/manifests"
fi

certified_bundle_file="./bundle/operator-certified-${VERSION}.tgz"
echo "Generating certified bundle"
tar -czvf "${certified_bundle_file}" "./bundle/${VERSION}"

if [[ "${BUILD_DOCKER_IMAGES}" == "true" ]]; then
  docker build --platform "${DOCKER_PLATFORM}" -f "./bundle/${VERSION}/bundle.Dockerfile" -t "${CERTIFIED_BUNDLE_IMAGE}" .
  docker push "${CERTIFIED_BUNDLE_IMAGE}"
fi
