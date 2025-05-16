#!/usr/bin/env bash

set -Eeou pipefail

# This script prepares and publishes OpenShift bundles and catalog source for operator upgrade tests using OLM.
# It enables testing two upgrade scenarios:
#   1. Migration from the latest release of MEKO (MongoDB Enterprise Kubernetes Operator)
#      to the current version of MCK (MongoDB Controllers for Kubernetes)
#      by uninstalling MEKO and installing MCK.
#   2. Upgrade from the latest release of MCK to the current version of MCK.
#
# The script builds a test catalog with:
#   - MEKO package (mongodb-enterprise) with one channel:
#       - stable channel: latest release of MEKO
#   - MCK package (mongodb-kubernetes) with three channels:
#       - stable channel: latest release of MCK
#       - fast channel: current version of MCK with an upgrade path from stable
#       - migration channel: current version of MCK. This will be used to test
#         migration from MEKO to MCK by uninstalling MEKO and installing MCK
#         so this channel has an entry without the replaces field.
#
# Required env vars:
#   - BASE_REPO_URL (or REGISTRY for EVG run)
#   - VERSION_ID (set in EVG as patch-id)

# for get_operator_helm_values function
source scripts/funcs/operator_deployment
source scripts/funcs/printing
source scripts/dev/set_env_context.sh

increment_version() {
  IFS=. read -r major minor patch <<<"$1"
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
  tr ' ' '\n' <<<"${helm_values[*]}"

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
  operator_name=$2
  # In this specific case, we don't want to use find as ls sorts things lexicographically - we need this!
  # shellcheck disable=SC2012
  ls -r "${certified_operators_cloned_repo}/operators/${operator_name}" | sed -n '2 p'
}

# Clones git_repo_url, builds the bundle in the given version and publishes it to bundle_image_url.
function build_bundle_from_git_repo() {
  tmpdir=$1
  operator_name=$2
  version=$3
  bundle_image_url=$4

  pushd "${tmpdir}"
  mkdir -p "bundle"

  mv "operators/${operator_name}/${version}" "bundle/"
  docker build --platform "${DOCKER_PLATFORM}" -f "./bundle/${version}/bundle.Dockerfile" -t "${bundle_image_url}" .
  docker push "${bundle_image_url}"
  popd
}

function init_test_catalog() {
  temp_dir=$1

  # The OPM tool generates the Dockerfile one directory higher then specified
  catalog_dir="${temp_dir}/operator-test-catalog/operator-test-catalog"
  mkdir -p "${catalog_dir}"

  opm generate dockerfile "${catalog_dir}"

  echo "${catalog_dir}"
}

function generate_mck_catalog_metadata() {
  catalog_dir=$1
  mck_package_name=$2
  latest_bundle_version=$3
  latest_bundle_image=$4
  current_bundle_version=$5
  current_bundle_image=$6
  meko_package_name=$7

  catalog_yaml="${catalog_dir}/${mck_package_name}.yaml"

  echo "Generating catalog metadata for ${mck_package_name} in ${catalog_yaml}"

  opm init "${mck_package_name}" \
    --default-channel="stable" \
    --output=yaml \
    >"${catalog_yaml}"

  echo "Adding current unreleased ${current_bundle_image} to the catalog"
  opm render "${current_bundle_image}" --output=yaml >>"${catalog_yaml}"

  echo "Adding latest release ${latest_bundle_image} to the catalog"
  opm render "${latest_bundle_image}" --output=yaml >>"${catalog_yaml}"

  echo "Adding latest MCK release into STABLE channel to ${catalog_yaml}"
  echo "---
schema: olm.channel
package: ${mck_package_name}
name: stable
entries:
  - name: ${mck_package_name}.v${latest_bundle_version}" >>"${catalog_yaml}"

  echo "Adding current MCK version replacing the latest MCK version into FAST channel to ${catalog_yaml}"
  echo "---
schema: olm.channel
package: ${mck_package_name}
name: fast
entries:
  - name: ${mck_package_name}.v${current_bundle_version}
    replaces: ${mck_package_name}.v${latest_bundle_version}" >>"${catalog_yaml}"

  echo "Adding current MCK version replacing the latest MEKO version into MIGRATION channel to ${catalog_yaml}"
  echo "---
schema: olm.channel
package: ${mck_package_name}
name: migration
entries:
  - name: ${mck_package_name}.v${current_bundle_version}" >>"${catalog_yaml}"
}

function generate_meko_catalog_metadata() {
  catalog_dir=$1
  meko_package_name=$2
  latest_bundle_version=$3
  latest_bundle_image=$4

  catalog_yaml="${catalog_dir}/${meko_package_name}.yaml"

  echo "Generating catalog metadata for ${meko_package_name} in ${catalog_yaml}"

  # Stable - latest release
  opm init "${meko_package_name}" \
    --default-channel="stable" \
    --output=yaml \
    >"${catalog_yaml}"

  echo "Adding latest release ${latest_bundle_image} to the catalog"
  opm render "${latest_bundle_image}" --output=yaml >>"${catalog_yaml}"

  echo "Adding latest MEKO release into STABLE channel to ${catalog_yaml}"
  echo "---
schema: olm.channel
package: ${meko_package_name}
name: stable
entries:
  - name: ${meko_package_name}.v${latest_bundle_version}" >>"${catalog_yaml}"
}

function build_and_publish_test_catalog() {
  catalog_dir=$1
  catalog_image=$2

  echo "Validating catalog"
  opm validate "${catalog_dir}"
  echo "Catalog is valid"
  echo "Building catalog image"
  cd "${catalog_dir}" && cd ../
  docker build --platform "${DOCKER_PLATFORM}" . -f operator-test-catalog.Dockerfile -t "${catalog_image}"
  docker push "${catalog_image}"
  echo "Catalog has been build and published"
  cd -
}

title "Executing prepare-openshift-bundles-for-e2e.sh"

export BUILD_DOCKER_IMAGES=true
export DOCKER_PLATFORM=${DOCKER_PLATFORM:-"linux/amd64"}

mck_package_name="mongodb-kubernetes"
meko_package_name="mongodb-enterprise"

certified_operators_repo="https://github.com/redhat-openshift-ecosystem/certified-operators.git"
current_operator_version_from_release_json=$(jq -r .mongodbOperator <release.json)
current_incremented_operator_version_from_release_json=$(increment_version "${current_operator_version_from_release_json}")
current_incremented_operator_version_from_release_json_with_version_id="${current_incremented_operator_version_from_release_json}-${VERSION_ID:-"latest"}"
test_catalog_image="${base_repo_url}/mongodb-kubernetes-test-catalog:${current_incremented_operator_version_from_release_json_with_version_id}"
certified_repo_cloned="$(clone_git_repo_into_temp ${certified_operators_repo})"

mck_latest_released_operator_version="$(find_the_latest_certified_operator "${certified_repo_cloned}" "${mck_package_name}")"
meko_latest_released_operator_version="$(find_the_latest_certified_operator "${certified_repo_cloned}" "${meko_package_name}")"

meko_latest_certified_bundle_image="${base_repo_url}/mongodb-enterprise-operator-certified-bundle:${meko_latest_released_operator_version}"
mck_latest_certified_bundle_image="${base_repo_url}/mongodb-kubernetes-certified-bundle:${mck_latest_released_operator_version}"
current_bundle_image="${base_repo_url}/mongodb-kubernetes-certified-bundle:${current_incremented_operator_version_from_release_json_with_version_id}"

header "Configuration:"
echo "certified_operators_repo: ${certified_operators_repo}"
echo "certified_repo_cloned: ${certified_repo_cloned}"
echo "mck_latest_released_operator_version: ${mck_latest_released_operator_version:-"NONE"}"
echo "meko_latest_released_operator_version: ${meko_latest_released_operator_version}"
echo "current_incremented_operator_version_from_release_json: ${current_incremented_operator_version_from_release_json}"
echo "current_incremented_operator_version_from_release_json_with_version_id: ${current_incremented_operator_version_from_release_json_with_version_id}"
echo "test_catalog_image: ${test_catalog_image}"
echo "meko_latest_certified_bundle_image: ${meko_latest_certified_bundle_image}"
echo "mck_latest_certified_bundle_image: ${mck_latest_certified_bundle_image}"
echo "current_bundle_image: ${current_bundle_image}"
echo "BUILD_DOCKER_IMAGES: ${BUILD_DOCKER_IMAGES}"
echo "DOCKER_PLATFORM: ${DOCKER_PLATFORM}"

# Build latest published bundles form RedHat's certified operators repository.
header "Building MCK bundle:"
build_bundle_from_git_repo "${certified_repo_cloned}" "${mck_package_name}" "${mck_latest_released_operator_version}" "${mck_latest_certified_bundle_image}"

header "Building MEKO bundle:"
build_bundle_from_git_repo "${certified_repo_cloned}" "${meko_package_name}" "${meko_latest_released_operator_version}" "${meko_latest_certified_bundle_image}"

# Generate helm charts providing overrides for images to reference images build in EVG pipeline.
header "Building Helm charts:"
generate_helm_charts

# prepare openshift bundles the same way it's built in release process from the current sources and helm charts.
export CERTIFIED_BUNDLE_IMAGE=${current_bundle_image}
export VERSION="${current_incremented_operator_version_from_release_json}"
export OPERATOR_IMAGE="${OPERATOR_REGISTRY:-${REGISTRY}}/mongodb-kubernetes:${VERSION_ID}"
header "Preparing OpenShift bundles:"
scripts/evergreen/operator-sdk/prepare-openshift-bundles.sh

# publish two-channel catalog source to be used in e2e test.
header "Building and pushing the catalog:"
catalog_dir="$(init_test_catalog "${certified_repo_cloned}")"
generate_mck_catalog_metadata "${catalog_dir}" "${mck_package_name}" "${mck_latest_released_operator_version}" "${mck_latest_certified_bundle_image}" "${current_incremented_operator_version_from_release_json}" "${current_bundle_image}" "${meko_package_name}"
generate_meko_catalog_metadata "${catalog_dir}" "${meko_package_name}" "${meko_latest_released_operator_version}" "${meko_latest_certified_bundle_image}"
build_and_publish_test_catalog "${catalog_dir}" "${test_catalog_image}"

header "Cleaning up tmp directory"
rm -rf "${certified_repo_cloned}"
