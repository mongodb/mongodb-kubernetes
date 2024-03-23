#!/usr/bin/env bash

# script to update code snippets file from MEKO in docs repository
# Usage:
#   cd <dir where meko and docs repositories are cloned>
#   ./update_docs_snippets.sh
#
# To customize directories run
#   MEKO_DIR=<path to meko repository> DOCS_DIR=<path to docs repository> ./update_docs_snippets.sh
# Example:
#   MEKO_DIR=~/mdb/ops-manager-kubernetes DOCS_DIR=~/mdb/docs-k8s-operator ./update_docs_snippets.sh

set -eou pipefail

MEKO_DIR=${MEKO_DIR:-"ops-manager-kubernetes"}
MEKO_BRANCH=${MEKO_BRANCH:-"om-mc-gke"}
DOCS_DIR=${DOCS_DIR:-"docs-k8s-operator"}
DOCS_BRANCH=${DOCS_BRANCH:-"master"}

function prepare_repositories() {
  pushd "${MEKO_DIR}"
  git fetch
  git checkout "${MEKO_BRANCH}"

  if [[ -n "$(git status --porcelain)" ]]; then
    echo "${MEKO_DIR} has modified files, stashing..."
    git stash
  fi

  git reset --hard "origin/${MEKO_BRANCH}"

  popd

  pushd "${DOCS_DIR}"
  git fetch
  if [[ -n "$(git status --porcelain)" ]]; then
    echo "${DOCS_DIR} has modified files, stashing..."
    git stash
  fi

  git checkout "${DOCS_BRANCH}"
  git reset --hard "origin/${DOCS_BRANCH}"

  git checkout -b "meko-snippets-update-$(date "+%Y%m%d%H%M%S")"
  popd
}

function copy_files() {
  samples_dir=$1
  dst_dir="${DOCS_DIR}/source/includes/code-examples/${samples_dir}"
  src_dir="${MEKO_DIR}/public/samples/${samples_dir}"

  mkdir -p "${dst_dir}"

  cp -r "${src_dir}/code_snippets" "${dst_dir}"
  cp -r "${src_dir}/output" "${dst_dir}"
  cp "${src_dir}/env_variables.sh" "${dst_dir}"
}

function prepare_docs_pr() {
  pushd "${DOCS_DIR}"
  if [[ -z "$(git status --porcelain)" ]]; then
    echo "No changes to push"
    return 1
  fi

  git add "source/"
  git commit -m "Update sample files from MEKO"
  git push
  popd
}

pushd ../
prepare_repositories
copy_files "ops-manager-multi-cluster"
prepare_docs_pr
popd
