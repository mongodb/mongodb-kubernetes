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

MCK_DIR=${MCK_DIR:-"mongodb-kubernetes"}
MCK_BRANCH=${MCK_BRANCH:-"release-x.x.x"}
DOCS_DIR=${DOCS_DIR:-"docs-mck"}
DOCS_BRANCH=${DOCS_BRANCH:-"master"}

function prepare_repositories() {
  pushd "${MCK_DIR}"
  git fetch
  git checkout "${MCK_BRANCH}"

  if [[ -n "$(git status --porcelain)" ]]; then
    echo "${MCK_DIR} has modified files, stashing..."
    git stash
  fi

  git reset --hard "origin/${MCK_BRANCH}"

  popd

  pushd "${DOCS_DIR}"
  git fetch
  if [[ -n "$(git status --porcelain)" ]]; then
    echo "${DOCS_DIR} has modified files, stashing..."
    git stash
  fi

  git checkout "${DOCS_BRANCH}"
  git reset --hard "origin/${DOCS_BRANCH}"

  git checkout -b "MCK-snippets-update-$(date "+%Y%m%d%H%M%S")"
  popd
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

  git add "source/"
  git commit -m "Update sample files from MCK"
  git push
  popd
}

pushd ../
prepare_repositories

REF_ARCH_SRC_DIR="${MCK_DIR}/public/architectures"
REF_ARCH_DST_DIR="${DOCS_DIR}/source/includes/code-examples/reference-architectures"

copy_files "${REF_ARCH_SRC_DIR}/ops-manager-multi-cluster" "${REF_ARCH_DST_DIR}/ops-manager-multi-cluster"
copy_files "${REF_ARCH_SRC_DIR}/ops-manager-mc-no-mesh" "${REF_ARCH_DST_DIR}/ops-manager-mc-no-mesh"
copy_files "${REF_ARCH_SRC_DIR}/mongodb-sharded-multi-cluster" "${REF_ARCH_DST_DIR}/mongodb-sharded-multi-cluster"
copy_files "${REF_ARCH_SRC_DIR}/mongodb-sharded-mc-no-mesh" "${REF_ARCH_DST_DIR}/mongodb-sharded-mc-no-mesh"
copy_files "${REF_ARCH_SRC_DIR}/mongodb-replicaset-multi-cluster" "${REF_ARCH_DST_DIR}/mongodb-replicaset-multi-cluster"
copy_files "${REF_ARCH_SRC_DIR}/mongodb-replicaset-mc-no-mesh" "${REF_ARCH_DST_DIR}/mongodb-replicaset-mc-no-mesh"
copy_files "${REF_ARCH_SRC_DIR}/setup-multi-cluster/verify-connectivity" "${REF_ARCH_DST_DIR}/setup-multi-cluster/verify-connectivity"
copy_files "${REF_ARCH_SRC_DIR}/setup-multi-cluster/setup-gke" "${REF_ARCH_DST_DIR}/setup-multi-cluster/setup-gke"
copy_files "${REF_ARCH_SRC_DIR}/setup-multi-cluster/setup-istio" "${REF_ARCH_DST_DIR}/setup-multi-cluster/setup-istio"
copy_files "${REF_ARCH_SRC_DIR}/setup-multi-cluster/setup-operator" "${REF_ARCH_DST_DIR}/setup-multi-cluster/setup-operator"
copy_files "${REF_ARCH_SRC_DIR}/setup-multi-cluster/setup-cert-manager" "${REF_ARCH_DST_DIR}/setup-multi-cluster/setup-cert-manager"
copy_files "${REF_ARCH_SRC_DIR}/setup-multi-cluster/setup-externaldns" "${REF_ARCH_DST_DIR}/setup-multi-cluster/setup-externaldns"

DOCS_SNIPPETS_SRC_DIR="${MCK_DIR}/docs"
DOCS_SNIPPEES_DST_DIR="${DOCS_DIR}/source/includes/code-examples"
copy_files "${DOCS_SNIPPETS_SRC_DIR}/community-search/quick-start" "${DOCS_SNIPPEES_DST_DIR}/community-search/quick-start"

prepare_docs_pr
popd
