#!/usr/bin/env bash

set -Eeou pipefail

# This scripts prepares bundles and catalog source for operator upgrade tests using OLM.
# It builds and publishes the following docker images:
#   - certified bundle from the last published version of the operator, from RedHat's certified operators repository
#   - bundle from the current branch, referencing images built as part of EVG pipeline
#   - catalog source with two channels: stable - referencing last published version, fast - referencing bundle built from the current branch
#
# Required env vars:
#   - BASE_REPO_URL (or REGISTRY for EVG run)
#   - VERSION_ID (set in EVG as patch-id)

# for get_operator_helm_values function
source scripts/funcs/operator_deployment

increment_version() {
  IFS=. read -r major minor patch <<< "$1"
  ((patch++))
  echo "${major}.${minor}.${patch}"
}

if [[ "${REGISTRY:-""}" != "" ]]; then
  base_repo_url="${REGISTRY}"
else
  base_repo_url="${BASE_REPO_URL:-"268558157000.dkr.ecr.us-east-1.amazonaws.com/dev"}/ubi"
fi
current_operator_version=$(jq -r .mongodbOperator < release.json)
current_incremented_version=$(increment_version "${current_operator_version}")
incremented_version_with_version_id="${current_incremented_version}-${VERSION_ID:-"latest"}"
certified_catalog_image="${base_repo_url}/mongodb-enterprise-operator-certified-catalog:${incremented_version_with_version_id}"

export BUILD_DOCKER_IMAGES=true
export LATEST_CERTIFIED_BUNDLE_IMAGE="${base_repo_url}/mongodb-enterprise-operator-certified-bundle:${current_operator_version}"
export CURRENT_BUNDLE_IMAGE="${base_repo_url}/mongodb-enterprise-operator-certified-bundle:${incremented_version_with_version_id}"
export CURRENT_COMMUNITY_BUNDLE_IMAGE="${base_repo_url}/mongodb-enterprise-operator-community-bundle:${incremented_version_with_version_id}"
export DOCKER_PLATFORM=${DOCKER_PLATFORM:-"linux/amd64"}

CERTIFIED_OPERATORS_REPO="https://github.com/redhat-openshift-ecosystem/certified-operators.git"

# Generates helm charts the same way they are generatedas part of pre-commit hook.
# We also provide helm values override the same way it's done when installing the operator helm chart in e2e tests.
generate_helm_charts() {
  read -ra helm_values < <(get_operator_helm_values)
  echo "Passing overrides to helm values: "
  tr ' ' '\n' <<< "${helm_values[*]}"

  declare -a helm_set_values=()
  for param in "${helm_values[@]}"; do
    helm_set_values+=("--set" "${param}")
  done

  .githooks/pre-commit generate_standalone_yaml "${helm_set_values[@]}"
}

# Clones git_repo_url, builds the bundle in the given version and publishes it to bundle_image_url.
function build_bundle_from_git_repo() {
  git_repo_url=$1
  version=$2
  bundle_image_url=$3

  tmpdir=$(mktemp -d)
  git clone --depth 1 "${git_repo_url}" "${tmpdir}"
  cd "${tmpdir}"
  mkdir -p "bundle"
  mv "operators/mongodb-enterprise/${version}" "bundle/"
  docker build --platform "${DOCKER_PLATFORM}" -f "./bundle/${version}/bundle.Dockerfile" -t "${bundle_image_url}" .
  docker push "${bundle_image_url}"
  rm -rf "${tmpdir}"
  cd -
}

# Builds and publishes catalog source with two channels: stable and fast.
build_and_publish_catalog_with_two_channels() {
  latest_bundle_version=$1
  latest_bundle_image=$2
  current_bundle_version=$3
  current_bundle_image=$4
  catalog_image=$5
  echo "Building catalog with latest released bundle and the current one"

  temp_dir=$(mktemp -d)
  catalog_dir="${temp_dir}/mongodb-enterprise-operator-catalog"
  mkdir -p "${catalog_dir}"

  echo "Generating the dockerfile"
  opm generate dockerfile "${catalog_dir}"
  echo "Generating the catalog"

  # Stable - latest release, fast - current version
  opm init mongodb-enterprise \
  	--default-channel="stable" \
  	--output=yaml \
  	> "${catalog_dir}"/operator.yaml

  echo "Adding latest release ${latest_bundle_image} to the catalog"
  opm render "${latest_bundle_image}" --output=yaml >> "${catalog_dir}"/operator.yaml

  echo "Adding current unreleased ${current_bundle_image} to the catalog"
  opm render "${current_bundle_image}" --output=yaml >> "${catalog_dir}"/operator.yaml

  echo "Adding previous release channel as STABLE to ${catalog_dir}/operator.yaml"
  echo "---
schema: olm.channel
package: mongodb-enterprise
name: stable
entries:
  - name: mongodb-enterprise.v${latest_bundle_version}" >> "${catalog_dir}"/operator.yaml

  echo "Adding current version channel as FAST to ${catalog_dir}/operator.yaml"
  echo "---
schema: olm.channel
package: mongodb-enterprise
name: fast
entries:
  - name: mongodb-enterprise.v${current_bundle_version}
    skips:
      - mongodb-enterprise.v${latest_bundle_version}" >> "${catalog_dir}"/operator.yaml

  echo "Validating catalog"
  opm validate "${catalog_dir}"
  echo "Catalog is valid"
  echo "Building catalog image"
  cd "$(dirname "${catalog_dir}")" && docker build --platform "${DOCKER_PLATFORM}" . -f mongodb-enterprise-operator-catalog.Dockerfile -t "${catalog_image}"
  docker push "${catalog_image}"
  echo "Catalog has been build and published"
  cd -

  rm -rf "${temp_dir}"
}

# Build latest published bundle form RedHat's certified operators repository.
build_bundle_from_git_repo "${CERTIFIED_OPERATORS_REPO}" "${current_operator_version}" "${LATEST_CERTIFIED_BUNDLE_IMAGE}"

# Generate helm charts providing overrides for images to reference images build in EVG pipeline.
generate_helm_charts

# prepare openshift bundles the same way it's built in release process from the current sources and helm charts.
export CERTIFIED_BUNDLE_IMAGE=${CURRENT_BUNDLE_IMAGE}
export COMMUNITY_BUNDLE_IMAGE=${CURRENT_COMMUNITY_BUNDLE_IMAGE}
export VERSION="${current_incremented_version}"
export OPERATOR_IMAGE="${OPERATOR_REGISTRY:-${REGISTRY}}/mongodb-enterprise-operator:${VERSION_ID}"
scripts/evergreen/operator-sdk/prepare-openshift-bundles.sh

# publish two-channel catalog source to be used in e2e test.
build_and_publish_catalog_with_two_channels "${current_operator_version}" "${LATEST_CERTIFIED_BUNDLE_IMAGE}" "${current_incremented_version}" "${CURRENT_BUNDLE_IMAGE}" "${certified_catalog_image}"
