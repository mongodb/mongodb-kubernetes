#!/usr/bin/env bash

# script to update code snippets file from MEKO in docs repository
# Usage:
#   cd <ops-manager-kubernetes directory>
#   ./scripts/dev/update_docs_snippets.sh
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
  dst_dir="${DOCS_DIR}/source/includes/code-examples/reference-architectures/${samples_dir}"
  src_dir="${MEKO_DIR}/public/architectures/${samples_dir}"

  rm -rf "${dst_dir}"
  mkdir -p "${dst_dir}"

  cp -r "${src_dir}/code_snippets" "${dst_dir}"
  cp -r "${src_dir}/output" "${dst_dir}"
  cp "${src_dir}/env_variables.sh" "${dst_dir}" || true
  cp -r "${src_dir}/yamls" "${dst_dir}" || true
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
copy_files "ops-manager-mc-no-mesh"
copy_files "mongodb-sharded-multi-cluster"
copy_files "mongodb-sharded-mc-no-mesh"
copy_files "mongodb-replicaset-multi-cluster"
copy_files "mongodb-replicaset-mc-no-mesh"
copy_files "setup-multi-cluster/verify-connectivity"
copy_files "setup-multi-cluster/setup-gke"
copy_files "setup-multi-cluster/setup-istio"
copy_files "setup-multi-cluster/setup-operator"
copy_files "setup-multi-cluster/setup-cert-manager"
copy_files "setup-multi-cluster/setup-externaldns"
prepare_docs_pr
popd
