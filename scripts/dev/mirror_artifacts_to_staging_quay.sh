#!/bin/bash
#
# Script to publish (mirror) container images and helm charts to staging registry.
#

set -euo pipefail

test "${MDB_BASH_DEBUG:-0}" -eq 1 && set -x

source scripts/dev/set_env_context.sh
source scripts/funcs/printing

BASE_REPO_URL="268558157000.dkr.ecr.us-east-1.amazonaws.com/dev"
STAGING_BASE_URL="quay.io/mongodb/staging"

if [[ $# -lt 2 ]]; then
    echo "The tool mirrors images built in any given evg patch id (or latest) from ${BASE_REPO_URL} to ${STAGING_BASE_URL}"
    echo "It publishes helm oci image of the helm chart with chart version \"<prerelease helm version>-<version_id>\""
    echo "Usage: $0 <prerelease helm version> <version_id>"
    echo "Example: $0 1.4.0-prerelease 68b1a853973bae0007d5eaa0"
    echo ""
    exit 1
fi

helm_chart_version_prefix="$1"
operator_version="$2"
search_version=${3:-"latest"}

helm_chart_version="${helm_chart_version_prefix}-${operator_version}"

get_arch_digest() {
    local image="$1"
    local arch="$2"
    local manifest_json
    manifest_json=$(docker buildx imagetools inspect --raw "${image}" 2>/dev/null || echo '{}')

    local media_type
    media_type=$(echo "$manifest_json" | jq -r '.mediaType // empty')

    if [[ "$media_type" == *"manifest.list"* ]]; then
        # this is a multi-arch manifest
        local arch_digest
        arch_digest=$(echo "$manifest_json" | jq -r ".manifests[] | select(.platform.architecture == \"${arch}\" and .platform.os == \"linux\") | .digest")

        if [[ -n "$arch_digest" && "$arch_digest" != "null" ]]; then
            echo "$arch_digest"
            return 0
        fi
    elif [[ "${arch}" == "amd64" ]]; then
      # otherwise it must be a single-arch (image) manifest, so we return it only if we ask for amd64
      local arch_digest
      arch_digest="sha256:$(echo -n "$manifest_json" | sha256)"
      echo "$arch_digest"
    fi

    echo ""
    return 0
}

process_image() {
    local source_image="$1"
    local target_image="$2"

    echo "  Processing ${source_image}..."

    local digest_arm64
    local digest_amd64
    digest_arm64=$(get_arch_digest "${source_image}" arm64)
    digest_amd64=$(get_arch_digest "${source_image}" amd64)

    if [[ -n "${digest_amd64}" ]]; then
      docker pull "${source_image}@${digest_amd64}"
      docker tag "${source_image}@${digest_amd64}" "${target_image}-amd64"
      docker push "${target_image}-amd64"
    fi

    if [[ -n "${digest_arm64}" ]]; then
      docker pull "${source_image}@${digest_arm64}"
      docker tag "${source_image}@${digest_arm64}" "${target_image}-arm64"
      docker push "${target_image}-arm64"
    fi

    docker manifest create "${target_image}" ${digest_amd64:+--amend ${target_image}-amd64} ${digest_arm64:+--amend ${target_image}-arm64}
    docker manifest push "${target_image}"
}

publish_images() {
  local names=()
  local sources=()
  local destinations=()

  operator_images=(
      "mongodb-kubernetes"
      "mongodb-kubernetes-database"
      "mongodb-kubernetes-init-appdb"
      "mongodb-kubernetes-init-database"
      "mongodb-kubernetes-init-ops-manager"
      "mongodb-kubernetes-readinessprobe"
      "mongodb-kubernetes-operator-version-upgrade-post-start-hook"
  )

  if [[ -n "${search_version}" ]]; then
      names+=("mongodb-search")
      sources+=("901841024863.dkr.ecr.us-east-1.amazonaws.com/mongot-community/rapid-releases:${search_version}")
      destinations+=("${STAGING_BASE_URL}/mongodb-search:${search_version}")
  fi

  for image in "${operator_images[@]}"; do
      names+=("${image}")
      sources+=("${BASE_REPO_URL}/${image}:${operator_version}")
      destinations+=("${STAGING_BASE_URL}/${image}:${helm_chart_version}")
  done

  echo "Starting Docker image re-tagging and publishing to staging..."
  echo "Version ID: ${operator_version}"
  echo "Source repository: ${BASE_REPO_URL}"
  echo "Target repository: ${STAGING_BASE_URL}"
  echo ""

  for i in "${!names[@]}"; do
      process_image "${sources[$i]}" "${destinations[$i]}"
  done

  echo "=== SUMMARY ==="
  echo "All images have been successfully re-tagged and pushed to staging!"
  echo ""
  echo "Images processed:"
  for i in "${!names[@]}"; do
      echo "  ${names[$i]}: ${sources[$i]} -> ${destinations[$i]}"
  done
}

update_helm_values() {
  scripts/dev/run_python.sh scripts/evergreen/release/update_helm_values_files.py
  yq eval ".version = \"${helm_chart_version}\"" -i helm_chart/Chart.yaml
  echo "Updated helm_chart/Chart.yaml version to: ${helm_chart_version}"

  yq eval \
     ".registry.operator = \"${STAGING_BASE_URL}\" |
     .registry.database = \"${STAGING_BASE_URL}\" |
     .registry.initDatabase = \"${STAGING_BASE_URL}\" |
     .registry.initOpsManager = \"${STAGING_BASE_URL}\" |
     .registry.initAppDb = \"${STAGING_BASE_URL}\" |
     .registry.appDb = \"${STAGING_BASE_URL}\" |
     .registry.versionUpgradeHook = \"${STAGING_BASE_URL}\" |
     .registry.readinessProbe = \"${STAGING_BASE_URL}\"
  " -i helm_chart/values.yaml
  echo "Updated helm_chart/values.yaml registry to: ${STAGING_BASE_URL}"
}

prepare_helm_oci_image() {
  mkdir -p tmp
  helm package helm_chart -d tmp/
}

push_helm_oci_image() {
  export HELM_REGISTRY_CONFIG=~/.docker/config.json
  helm push "tmp/mongodb-kubernetes-${helm_chart_version}.tgz" "oci://${STAGING_BASE_URL}/helm-chart"
}

update_release_json() {
  if [[ ! -f "release.json" ]]; then
    echo "Error: release.json file not found"
    exit 1
  fi

  echo "Updating release.json with versions..."

  # Update operator and init versions
  jq --arg version "${helm_chart_version}" \
     --arg registry "${STAGING_BASE_URL}" \
    '.mongodbOperator = $version |
     .initDatabaseVersion = $version |
     .initOpsManagerVersion = $version |
     .initAppDbVersion = $version |
     .databaseImageVersion = $version' \
    release.json > release.json.tmp && mv release.json.tmp release.json

  # Update search community version
  jq --arg searchVersion "${search_version}" \
     --arg searchRepo "${STAGING_BASE_URL}" \
     --arg searchImageName "mongodb-search" \
    '.search.community.repo = $searchRepo |
    .search.community.name = $searchImageName |
    .search.community.version = $searchVersion' \
    release.json > release.json.tmp && mv release.json.tmp release.json

  echo "Updated release.json with:"
  echo "  - Operator versions: ${helm_chart_version}"
  echo "  - Search community version: ${MDB_SEARCH_COMMUNITY_VERSION}"
}

revert_changes_to_local_files() {
  echo "Reverting generated/updated files: helm_chart/ public/ config/ release.json"
  git checkout -- helm_chart/ public/ config/ release.json
}

publish_images
update_release_json
update_helm_values
prepare_helm_oci_image
push_helm_oci_image
revert_changes_to_local_files
