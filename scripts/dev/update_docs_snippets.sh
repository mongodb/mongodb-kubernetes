#!/usr/bin/env bash

# script to update code snippets file from MCK in docs repository
# Usage:
#   cd <mongodb-kubernetes directory>
#   ./scripts/dev/update_docs_snippets.sh
#
# To customize directories run
#   MCK_DIR=<path to MCK repository> DOCS_DIR=<path to docs repository> ./update_docs_snippets.sh
# Example:
#   MCK_DIR=~/mdb/mongodb-kubernetes DOCS_DIR=~/mdb/docs-k8s-operator ./update_docs_snippets.sh

set -eou pipefail
set -x

MCK_DIR=${MCK_DIR:-"mongodb-kubernetes"}
MCK_BRANCH=${MCK_BRANCH:-"master"}
DOCS_DIR=${DOCS_DIR:-"docs-mongodb-internal"}
DOCS_BRANCH=${DOCS_BRANCH:-"main"}
DOCS_VERSION=${DOCS_VERSION:-"upcoming"}
DOCS_INCLUDE_CODE_EXAMPLES_DIR="${DOCS_DIR}/content/kubernetes/${DOCS_VERSION}/source/includes/code-examples"

function prepare_repositories() {
  pushd "${DOCS_DIR}"
  git fetch
  if [[ -n "$(git status --porcelain)" ]]; then
    echo "${DOCS_DIR} has modified files, stashing..."
    git stash
  fi

  git checkout "${DOCS_BRANCH}"
  git reset --hard "origin/${DOCS_BRANCH}"

  git branch "MCK-snippets-update-${version_id}" || true
  git checkout "MCK-snippets-update-${version_id}"

  popd
}

function download_snippets_outputs() {
  dir=$1
  evg_version_id=$2
  echo "Downloading snippets outputs from s3 to ${dir}"
  aws s3 sync 's3://operator-e2e-artifacts/snippets_outputs/' "${dir}/" --exclude '*' --include "${evg_version_id}*"
  mkdir -p "${dir}/${evg_version_id}"
  cd "${dir}/${evg_version_id}"
  for f in *.tgz; do
    tar -xvf "${f}"
  done

  outputs_dir="scripts/code_snippets/tests/outputs"
  if [[ ! -d "${outputs_dir}" ]]; then
    echo "No snippets were downloaded"
    ls -al
    return 1
  fi
}

function copy_files() {
  local src_dir="$1"
  local dst_dir="$2"

  rm -rf "${dst_dir}"
  mkdir -p "${dst_dir}"

  cp -r "${src_dir}/code_snippets" "${dst_dir}" 2>/dev/null || true
  cp -r "${src_dir}/output" "${dst_dir}" 2>/dev/null || true
  cp "${src_dir}/env_variables.sh" "${dst_dir}" 2>/dev/null || true
  cp -r "${src_dir}/yamls" "${dst_dir}" 2>/dev/null || true
}

function prepare_docs_pr() {
  pushd "${DOCS_DIR}"
  if [[ -z "$(git status --porcelain)" ]]; then
    echo "No changes to push"
    return 1
  fi

  git add "${DOCS_INCLUDE_CODE_EXAMPLES_DIR}"
  git commit -m "Update sample files from MCK"
  git push
  popd
}

pushd ../
prepare_repositories

tmp_dir=$(mktemp -d)
download_snippets_outputs "${tmp_dir}" "${version_id}"
outputs_dir="${tmp_dir}/${version_id}/scripts/code_snippets/tests/outputs"

for test_dir in ${outputs_dir}/test_*; do
  echo "Replacing outputs for test: ${test_dir}"
  rm -rf "${DOCS_INCLUDE_CODE_EXAMPLES_DIR}/outputs/$(basename "${test_dir}")"
  cp -r "${test_dir}" "${DOCS_INCLUDE_CODE_EXAMPLES_DIR}/outputs/$(basename "${test_dir}")"
done

echo "${outputs_dir}"
tree "${outputs_dir}"

rm -rf "${DOCS_INCLUDE_CODE_EXAMPLES_DIR}/reference-architectures"
cp -r "${MCK_DIR}/public/architectures" "${DOCS_INCLUDE_CODE_EXAMPLES_DIR}/reference-architectures"

rm -rf "${DOCS_INCLUDE_CODE_EXAMPLES_DIR}/search"
cp -r "${MCK_DIR}/docs/search" "${DOCS_INCLUDE_CODE_EXAMPLES_DIR}/search"

prepare_docs_pr
popd
