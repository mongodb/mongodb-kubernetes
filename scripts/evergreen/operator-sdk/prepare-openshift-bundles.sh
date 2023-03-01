#!/usr/bin/env bash
set -Eeou pipefail

# This script prepares two openshift bundles tgz: certified and community.

mkdir -p bundle

version=${VERSION:-$(jq -r .mongodbOperator < release.json)}
echo "Generating openshift bundle for version ${version}"
make bundle VERSION="${version}" IMG="quay.io/mongodb/mongodb-enterprise-operator:${version}"

mv bundle.Dockerfile "./bundle/${version}/bundle.Dockerfile"

minimum_supported_openshift_version=$(jq -r .openshift.minimumSupportedVersion < release.json)
bundle_annotations_file="bundle/${version}/metadata/annotations.yaml"
bundle_dockerfile="bundle/${version}/bundle.Dockerfile"

echo "Adding minimum_supported_openshift_version annotation to ${bundle_annotations_file}"
yq e ".annotations.\"com.redhat.openshift.versions\" = \"v${minimum_supported_openshift_version}\"" -i "${bundle_annotations_file}"

echo "Adding minimum_supported_openshift_version annotation to ${bundle_dockerfile}"
echo "LABEL com.redhat.openshift.versions=\"v${minimum_supported_openshift_version}\"" >> "${bundle_dockerfile}"

community_bundle_file="./bundle/operator-community-${version}.tgz"
echo "Generating community bundle"
tar -czvf "${community_bundle_file}" "./bundle/${version}"

echo "Running digest pinning for certified bundle"
operator-manifest-tools pinning pin -v --resolver crane "bundle/${version}/manifests"

certified_bundle_file="./bundle/operator-certified-${version}.tgz"
echo "Generating certified bundle"
tar -czvf "${certified_bundle_file}" "./bundle/${version}"
