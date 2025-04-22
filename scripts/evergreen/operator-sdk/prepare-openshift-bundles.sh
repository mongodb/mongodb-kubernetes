#!/usr/bin/env bash
set -Eeou pipefail

source scripts/dev/set_env_context.sh

# This script prepares two openshift bundles tgz: certified and community.

RELEASE_JSON_PATH=${RELEASE_JSON_PATH:-"release.json"}
VERSION=${VERSION:-$(jq -r .mongodbOperator < "${RELEASE_JSON_PATH}")}
BUILD_DOCKER_IMAGES=${BUILD_DOCKER_IMAGES:-"false"}
OPERATOR_IMAGE=${OPERATOR_IMAGE:-"quay.io/mongodb/mongodb-enterprise-operator-ubi:${VERSION}"}
DOCKER_PLATFORM=${DOCKER_PLATFORM:-"linux/amd64"}

mkdir -p bundle

echo "Generating openshift bundle for version ${VERSION}"
make bundle VERSION="${VERSION}"

mv bundle.Dockerfile "./bundle/${VERSION}/bundle.Dockerfile"

minimum_supported_openshift_version=$(jq -r .openshift.minimumSupportedVersion < "${RELEASE_JSON_PATH}")
bundle_annotations_file="bundle/${VERSION}/metadata/annotations.yaml"
bundle_dockerfile="bundle/${VERSION}/bundle.Dockerfile"
bundle_csv_file="bundle/${VERSION}/manifests/mongodb-kubernetes.clusterserviceversion.yaml"

echo "Aligning metadata.annotations.containerImage version with deployment's image in ${bundle_csv_file}"
operator_deployment_image=$(yq '.spec.install.spec.deployments[0].spec.template.spec.containers[0].image' < "${bundle_csv_file}")
yq e ".metadata.annotations.containerImage = \"${operator_deployment_image}\"" -i "${bundle_csv_file}"

echo "Edited CSV: ${bundle_csv_file}"
cat "${bundle_csv_file}"

echo "Adding minimum_supported_openshift_version annotation to ${bundle_annotations_file}"
yq e ".annotations.\"com.redhat.openshift.versions\" = \"v${minimum_supported_openshift_version}\"" -i "${bundle_annotations_file}"

echo "Adding minimum_supported_openshift_version annotation to ${bundle_dockerfile}"
echo "LABEL com.redhat.openshift.versions=\"v${minimum_supported_openshift_version}\"" >> "${bundle_dockerfile}"

PATH="${PATH}:bin"

echo "Running digest pinning for certified bundle"
# This can fail during the release because the latest image is not available yet and will be available the next day/next daily rebuild.
# We decided to skip digest pinning during the as it is a post-processing step and it should be fine to skip it when testing OLM during the release.
if [[ "${DIGEST_PINNING_ENABLED:-"true"}" == "true" ]]; then
  operator_image=$(yq ".spec.install.spec.deployments[0].spec.template.spec.containers[0].image" < ./bundle/"${VERSION}"/manifests/mongodb-kubernetes.clusterserviceversion.yaml)
  operator_annotation_image=$(yq ".metadata.annotations.containerImage" < ./bundle/"${VERSION}"/manifests/mongodb-kubernetes.clusterserviceversion.yaml)
  if [[ "${operator_image}" != "${operator_annotation_image}" ]]; then
    echo "Inconsistent operator images in CSV (.spec.install.spec.deployments[0].spec.template.spec.containers[0].image=${operator_image}, .metadata.annotations.containerImage=${operator_annotation_image})"
    cat ./bundle/"${VERSION}"/manifests/mongodb-kubernetes.clusterserviceversion.yaml
    exit 1
  fi

  if docker manifest inspect "${operator_image}" > /dev/null 2>&1; then
      echo "Running digest pinning, since the operator image: ${operator_image} exists"
      operator-manifest-tools pinning pin -v --resolver skopeo "bundle/${VERSION}/manifests"
    else
      echo "Skipping pinning tools, since the operator image: ${operator_image} or ${operator_annotation_image} are missing and we are most likely in a release"
  fi
fi

certified_bundle_file="./bundle/mck-operator-certified-${VERSION}.tgz"
echo "Generating certified bundle"
tar -czvf "${certified_bundle_file}" "./bundle/${VERSION}"

if [[ "${BUILD_DOCKER_IMAGES}" == "true" ]]; then
  docker build --platform "${DOCKER_PLATFORM}" -f "./bundle/${VERSION}/bundle.Dockerfile" -t "${CERTIFIED_BUNDLE_IMAGE}" .
  docker push "${CERTIFIED_BUNDLE_IMAGE}"
fi
