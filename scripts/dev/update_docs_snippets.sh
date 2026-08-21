#!/usr/bin/env bash

# Downloads code-snippet outputs from S3 and creates a PR in the docs repository.
#
# Prerequisites:
#  * OUTPUTS_VERSION_ID - Evergreen version/patch ID from a snippets run (archives outputs to S3).
#  * AWS credentials with read access to s3://operator-e2e-artifacts/
#
# The docs repo can be provided either way:
#  * Pre-cloned at DOCS_DIR (default: docs-mongodb-internal, relative to parent dir).
#  * Or auto-cloned: set GITHUB_TOKEN and the script clones ${DOCS_REPO} to a temp dir.
#
# PR creation requires GITHUB_TOKEN and the gh CLI.
#
# Usage (manual):
#   cd <mongodb-kubernetes directory>
#   OUTPUTS_VERSION_ID=<patch-id> \
#   DOCS_DIR=~/mdb/docs-mongodb-internal \
#     ./scripts/dev/update_docs_snippets.sh
#
# Usage (auto-clone + PR, no pre-cloned repo needed):
#   cd <mongodb-kubernetes directory>
#   OUTPUTS_VERSION_ID=<patch-id> \
#   GITHUB_TOKEN=$(gh auth token) \
#     ./scripts/dev/update_docs_snippets.sh
#
# To customize directories:
#   MCK_DIR=<path to MCK repository> \
#   DOCS_DIR=<path to docs repository> \
#     scripts/dev/update_docs_snippets.sh
#
# Env vars:
#   OUTPUTS_VERSION_ID  Evergreen version/patch ID (required; also accepts version_id).
#   MCK_DIR             Path to MCK repo (default: mongodb-kubernetes).
#   DOCS_DIR            Path to docs repo (default: docs-mongodb-internal).
#   DOCS_REPO           GitHub org/repo for docs (default: 10gen/docs-mongodb-internal).
#   DOCS_BRANCH         Base branch in docs repo (default: main).
#   DOCS_VERSION        Version dir in docs repo (default: upcoming).
#   DOCS_PR_BRANCH      Branch name for the PR (default: MCK-snippets-update-${OUTPUTS_VERSION_ID}).
#   DOCS_PR_TITLE       PR title (default: "Update MCK code snippets for ${OUTPUTS_VERSION_ID}").
#   GITHUB_TOKEN        GitHub token for auto-clone and PR creation.
#   GH_APP_ID           GitHub App ID for token minting (fallback when GITHUB_TOKEN is unset).
#   GH_APP_INSTALLATION_ID  GitHub App installation ID.
#   GH_APP_PEM_B64      Base64-encoded GitHub App PEM private key.
#   NO_PUSH             Set to 1 to skip git push.
#   DRY_RUN             Set to 1 to skip push and PR creation.

set -eou pipefail

test "${MDB_BASH_DEBUG:-0}" -eq 1 && set -x

OUTPUTS_VERSION_ID=${OUTPUTS_VERSION_ID:-${version_id:?}}
# MCK repo dir
MCK_DIR=${MCK_DIR:-"mongodb-kubernetes"}
# Docs repo dir
DOCS_DIR=${DOCS_DIR:-"docs-mongodb-internal"}
# Branch on which to base snippets branch
DOCS_BRANCH=${DOCS_BRANCH:-"main"}
# Version directory in docs repo (upcoming, current, etc.)
DOCS_VERSION=${DOCS_VERSION:-"upcoming"}
# Branch name for snippets
DOCS_PR_BRANCH=${DOCS_PR_BRANCH:-"MCK-snippets-update-${OUTPUTS_VERSION_ID}"}
# Set NO_PUSH=1 to skip pushing after commit
NO_PUSH=${NO_PUSH:-0}
# GitHub repo for docs PR (org/repo)
DOCS_REPO=${DOCS_REPO:-"10gen/docs-mongodb-internal"}
# PR title
DOCS_PR_TITLE=${DOCS_PR_TITLE:-"Update MCK code snippets for ${OUTPUTS_VERSION_ID}"}
# Dry-run mode: skip push and PR creation
DRY_RUN=${DRY_RUN:-0}

# If GITHUB_TOKEN is not set but GitHub App credentials are available, mint a token.
if [[ -z "${GITHUB_TOKEN:-}" && -n "${GH_APP_PEM_B64:-}" ]]; then
  echo "Minting GitHub App token..."
  GITHUB_TOKEN="$(scripts/mckci github app-token \
    --app-id "${GH_APP_ID:-}" \
    --installation-id "${GH_APP_INSTALLATION_ID:-}" \
    --pem-base64 "${GH_APP_PEM_B64}")"
  export GITHUB_TOKEN
fi

# Auto-clone the docs repo if DOCS_DIR doesn't exist and we have a GitHub token.
if [[ -n "${GITHUB_TOKEN:-}" ]] && [[ ! -d "${DOCS_DIR}" ]]; then
  echo "DOCS_DIR (${DOCS_DIR}) not found; cloning ${DOCS_REPO}..."
  DOCS_DIR=$(mktemp -d)
  git clone "https://${GITHUB_TOKEN}@github.com/${DOCS_REPO}.git" "${DOCS_DIR}"
elif [[ ! -d "${DOCS_DIR}" ]]; then
  echo "DOCS_DIR (${DOCS_DIR}) not found and GITHUB_TOKEN not set; cannot proceed." >&2
  echo "Either set GITHUB_TOKEN for auto-clone or clone ${DOCS_REPO} to ${DOCS_DIR} manually." >&2
  exit 1
fi

docs_include_code_examples_dir="${DOCS_DIR}/content/kubernetes/${DOCS_VERSION}/source/includes/code-examples"

function prepare_repositories() {
  pushd "${DOCS_DIR}"
  git fetch
  if [[ -n "$(git status --porcelain)" ]]; then
    echo "${DOCS_DIR} has modified files, stashing..."
    git stash
  fi

  # If the branch already exists locally, reuse it
  if git show-ref --verify --quiet "refs/heads/${DOCS_PR_BRANCH}"; then
    echo "Reusing existing branch ${DOCS_PR_BRANCH}"
    git checkout "${DOCS_PR_BRANCH}"
  else
    git checkout "${DOCS_BRANCH}"
    git reset --hard "origin/${DOCS_BRANCH}"
    git checkout -b "${DOCS_PR_BRANCH}"
  fi

  popd
}

function download_snippets_outputs() {
  dir=$1
  evg_version_id=$2
  echo "Downloading snippets outputs from s3 to ${dir}"
  aws s3 sync 's3://operator-e2e-artifacts/snippets_outputs/' "${dir}/" --exclude '*' --include "${evg_version_id}*"
  mkdir -p "${dir}"
  cd "${dir}"
  for f in *.tgz; do
    if [[ -f ${f} ]]; then
      tar -xvf "${f}"
    fi
  done

  outputs_dir="scripts/code_snippets/tests/outputs"
  if [[ ! -d "${outputs_dir}" ]]; then
    echo "No snippets were downloaded"
    return 1
  fi
}

function prepare_docs_pr() {
  pushd "${DOCS_DIR}"
  if [[ -z "$(git status --porcelain)" ]]; then
    echo "No changes to push"
    return 1
  fi

  git add "${docs_include_code_examples_dir}"
  git commit -m "Update sample files from MCK"
  if [[ "${DRY_RUN}" == "1" ]]; then
    echo "[DRY-RUN] Would push branch ${DOCS_PR_BRANCH} to ${DOCS_REPO}"
  elif [[ "${NO_PUSH}" != "1" ]]; then
    git push --set-upstream origin "${DOCS_PR_BRANCH}"
  else
    echo "Skipping push (NO_PUSH=1)"
  fi
  popd
}

function create_docs_pr() {
  if [[ -z "${GITHUB_TOKEN:-}" ]]; then
    echo "GITHUB_TOKEN not set; skipping PR creation."
    return 0
  fi

  if [[ "${DRY_RUN}" == "1" ]]; then
    echo "[DRY-RUN] Would create PR: ${DOCS_PR_BRANCH} → ${DOCS_BRANCH} in ${DOCS_REPO}"
    echo "[DRY-RUN] Title: ${DOCS_PR_TITLE}"
    return 0
  fi

  if ! command -v gh &>/dev/null; then
    echo "gh CLI not found; cannot create PR." >&2
    return 1
  fi

  pushd "${DOCS_DIR}"
  # Authenticate gh with the token
  echo "${GITHUB_TOKEN}" | gh auth login --with-token 2>/dev/null || true
  gh pr create \
    --repo "${DOCS_REPO}" \
    --base "${DOCS_BRANCH}" \
    --head "${DOCS_PR_BRANCH}" \
    --title "${DOCS_PR_TITLE}" \
    --body "Auto-generated from MCK snippets run \`${OUTPUTS_VERSION_ID}\`."
  popd
}

pushd ../
prepare_repositories

tmp_dir=$(mktemp -d)
if download_snippets_outputs "${tmp_dir}" "${OUTPUTS_VERSION_ID}"; then
  outputs_dir="${tmp_dir}/scripts/code_snippets/tests/outputs"

  for test_dir in "${outputs_dir}"/test_*; do
    echo "Replacing outputs for test: ${test_dir}"
    rm -rf "${docs_include_code_examples_dir}/outputs/$(basename "${test_dir}")"
    cp -r "${test_dir}" "${docs_include_code_examples_dir}/outputs/$(basename "${test_dir}")"
  done

  echo "${outputs_dir}"
  tree "${outputs_dir}"
fi

rsync -a --delete --exclude='README.md' --exclude='env_variables_*.sh' \
  "${MCK_DIR}/public/architectures/" "${docs_include_code_examples_dir}/reference-architectures/"

rsync -a --delete --exclude='README.md' --exclude='env_variables_*.sh' \
  "${MCK_DIR}/docs/search/" "${docs_include_code_examples_dir}/search/"

prepare_docs_pr
create_docs_pr
popd
