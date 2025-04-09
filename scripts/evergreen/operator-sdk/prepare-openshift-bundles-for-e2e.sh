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
source scripts/funcs/printing
source scripts/dev/set_env_context.sh

increment_version() {
  IFS=. read -r major minor patch <<< "$1"
  ((patch++))
  echo "${major}.${minor}.${patch}"
}

if [[ "${REGISTRY:-""}" != "" ]]; then
  base_repo_url="${REGISTRY}"
else
  base_repo_url="${BASE_REPO_URL:-"268558157000.dkr.ecr.us-east-1.amazonaws.com/dev"}"
fi

# Generates helm charts the same way they are generates part of pre-commit hook.
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

function clone_git_repo_into_temp() {
  git_repo_url=$1
  tmpdir=$(mktemp -d)
  git clone --depth 1 "${git_repo_url}" "${tmpdir}"
  echo "${tmpdir}"
}

function find_the_latest_certified_operator() {
  certified_operators_cloned_repo=$1
  # In this specific case, we don't want to use find as ls sorts things lexicographically - we need this!
  # shellcheck disable=SC2012
  ls -r "${certified_operators_cloned_repo}/operators/mongodb-enterprise" | sed -n '2 p'
}

# Clones git_repo_url, builds the bundle in the given version and publishes it to bundle_image_url.
function build_bundle_from_git_repo() {
  tmpdir=$1
  version=$2
  bundle_image_url=$3

  pushd "${tmpdir}"
  mkdir -p "bundle"

  mv "operators/mongodb-enterprise/${version}" "bundle/"
  docker build --platform "${DOCKER_PLATFORM}" -f "./bundle/${version}/bundle.Dockerfile" -t "${bundle_image_url}" .
  docker push "${bundle_image_url}"
  popd
}

# Builds and publishes catalog source with two channels: stable and fast.
build_and_publish_catalog_with_two_channels() {
  temp_dir=$1
  latest_bundle_version=$2
  latest_bundle_image=$3
  current_bundle_version=$4
  current_bundle_image=$5
  catalog_image=$6
  echo "Building catalog with latest released bundle and the current one"

  # The OPM tool generates the Dockerfile one directory higher then specified
  catalog_dir="${temp_dir}/mongodb-enterprise-operator-catalog/mongodb-enterprise-operator-catalog"
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
    replaces: mongodb-enterprise.v${latest_bundle_version}" >> "${catalog_dir}"/operator.yaml

  echo "Validating catalog"
  opm validate "${catalog_dir}"
  echo "Catalog is valid"
  echo "Building catalog image"
  cd "${catalog_dir}" && cd ../
  docker build --platform "${DOCKER_PLATFORM}" . -f mongodb-enterprise-operator-catalog.Dockerfile -t "${catalog_image}"
  docker push "${catalog_image}"
  echo "Catalog has been build and published"
  cd -
}

title "Executing prepare-openshift-bundles-for-e2e.sh"

export BUILD_DOCKER_IMAGES=true
export DOCKER_PLATFORM=${DOCKER_PLATFORM:-"linux/amd64"}
CERTIFIED_OPERATORS_REPO="https://github.com/redhat-openshift-ecosystem/certified-operators.git"

certified_repo_cloned="$(clone_git_repo_into_temp ${CERTIFIED_OPERATORS_REPO})"
latest_released_operator_version="$(find_the_latest_certified_operator "${certified_repo_cloned}")"
current_operator_version_from_release_json=$(jq -r .mongodbOperator < release.json)
current_incremented_operator_version_from_release_json=$(increment_version "${current_operator_version_from_release_json}")
current_incremented_operator_version_from_release_json_with_version_id="${current_incremented_operator_version_from_release_json}-${VERSION_ID:-"latest"}"
certified_catalog_image="${base_repo_url}/mongodb-enterprise-operator-certified-catalog:${current_incremented_operator_version_from_release_json_with_version_id}"

export LATEST_CERTIFIED_BUNDLE_IMAGE="${base_repo_url}/mongodb-enterprise-operator-certified-bundle:${latest_released_operator_version}"
export CURRENT_BUNDLE_IMAGE="${base_repo_url}/mongodb-enterprise-operator-certified-bundle:${current_incremented_operator_version_from_release_json_with_version_id}"
export CURRENT_COMMUNITY_BUNDLE_IMAGE="${base_repo_url}/mongodb-enterprise-operator-community-bundle:${current_incremented_operator_version_from_release_json_with_version_id}"


header "Configuration:"
echo "certified_repo_cloned: ${certified_repo_cloned}"
echo "latest_released_operator_version: ${latest_released_operator_version}"
echo "current_incremented_operator_version_from_release_json: ${current_incremented_operator_version_from_release_json}"
echo "current_incremented_operator_version_from_release_json_with_version_id: ${current_incremented_operator_version_from_release_json_with_version_id}"
echo "LATEST_CERTIFIED_BUNDLE_IMAGE: ${LATEST_CERTIFIED_BUNDLE_IMAGE}"
echo "CURRENT_BUNDLE_IMAGE: ${CURRENT_BUNDLE_IMAGE}"
echo "CURRENT_COMMUNITY_BUNDLE_IMAGE: ${CURRENT_COMMUNITY_BUNDLE_IMAGE}"
echo "BUILD_DOCKER_IMAGES: ${BUILD_DOCKER_IMAGES}"
echo "DOCKER_PLATFORM: ${DOCKER_PLATFORM}"
echo "CERTIFIED_OPERATORS_REPO: ${CERTIFIED_OPERATORS_REPO}"

# Build latest published bundle form RedHat's certified operators repository.
header "Building bundle:"
build_bundle_from_git_repo "${certified_repo_cloned}" "${latest_released_operator_version}" "${LATEST_CERTIFIED_BUNDLE_IMAGE}"

# Generate helm charts providing overrides for images to reference images build in EVG pipeline.
header "Building Helm charts:"
generate_helm_charts

# prepare openshift bundles the same way it's built in release process from the current sources and helm charts.
export CERTIFIED_BUNDLE_IMAGE=${CURRENT_BUNDLE_IMAGE}
export COMMUNITY_BUNDLE_IMAGE=${CURRENT_COMMUNITY_BUNDLE_IMAGE}
export VERSION="${current_incremented_operator_version_from_release_json}"
export OPERATOR_IMAGE="${OPERATOR_REGISTRY:-${REGISTRY}}/mongodb-enterprise-operator-ubi:${VERSION_ID}"
header "Preparing OpenShift bundles:"
scripts/evergreen/operator-sdk/prepare-openshift-bundles.sh

# publish two-channel catalog source to be used in e2e test.
header "Building and pushing the catalog:"
build_and_publish_catalog_with_two_channels "${certified_repo_cloned}" "${latest_released_operator_version}" "${LATEST_CERTIFIED_BUNDLE_IMAGE}" "${current_incremented_operator_version_from_release_json}" "${CURRENT_BUNDLE_IMAGE}" "${certified_catalog_image}"

header "Cleaning up tmp directory"
rm -rf "${certified_repo_cloned}"
