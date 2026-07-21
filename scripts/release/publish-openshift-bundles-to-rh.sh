#!/usr/bin/env bash
# Publishes the OpenShift OLM bundle to the RedHat operator repositories.
#
# It downloads the unified bundle tarball (produced and uploaded by the
# prepare_and_upload_openshift_bundles Evergreen task) and, for each RedHat fork,
# pushes a branch containing the bundle laid out under operators/<package>/<version>/.
#
# It intentionally does NOT open the pull requests: it prints the "create PR" URLs and
# the manual review checklist so a human can raise the DRAFT PRs and drive the review.
#
# The same bundle is used for both the certified-operators and the
# community-operators repositories (the file contents are identical between them).
# The "certified" term is used in the tarball file for historical reasons.
#
# Usage (runnable standalone from a CLI or from Evergreen):
#   GH_TOKEN=<token-with-write-to-forks> VERSION=1.3.0 RH_DRYRUN=true \
#     scripts/release/publish-openshift-bundles-to-rh.sh
#
# Environment:
#   GH_TOKEN    (optional) token with write access to the mongodb-forks repos. If unset, it is
#               resolved in order from: 1) the gh CLI's cached login (`gh auth token`), then
#               2) minting a fresh installation token from a dedicated GitHub App via
#               `scripts/mckci github app-token` (see RH_GH_APP_* below). If none of these
#               resolve a token, the script falls back to ambient git credentials.
#   RH_GH_APP_ID                (required for App-token fallback) GitHub App ID.
#   RH_GH_APP_INSTALLATION_ID   (required for App-token fallback) installation ID for the app
#                                on the mongodb-forks org.
#   RH_GH_APP_PEM_B64           (required for App-token fallback) base64-encoded PEM private
#                                key for the app. In Evergreen this comes from the Private
#                                project variable `rh_gh_app_pem_b64`.
#   VERSION     (optional) operator version; defaults to .mongodbOperator in release.json.
#   RH_DRYRUN   (optional) "true" (default) performs a --dry-run push; "false" pushes.
#   S3_BUNDLES_URL (optional) base URL of the bundles bucket.
#   PACKAGE     (optional) operator package dir name; defaults to mongodb-kubernetes.

set -Eeou pipefail
# Opt-in command tracing for debugging: MDB_DEBUG=1
test "${MDB_DEBUG:-0}" -eq 1 && set -x

VERSION="${VERSION:-$(jq -r .mongodbOperator < release.json)}"
RH_DRYRUN="${RH_DRYRUN:-true}"
PACKAGE="${PACKAGE:-mongodb-kubernetes}"
S3_BUNDLES_URL="${S3_BUNDLES_URL:-https://operator-e2e-bundles.s3.amazonaws.com/bundles}"

# GH_TOKEN is optional: if unset we fall back, in order, to 1) the gh CLI token (if available),
# 2) minting a fresh installation token from a dedicated GitHub App (see RH_GH_APP_* below), and
# ultimately to the ambient git credentials (credential helper / cached login). This keeps
# the script usable from a logged-in CLI while still accepting an explicit token in Evergreen.
GH_TOKEN="${GH_TOKEN:-}"
if [[ -z "${GH_TOKEN}" ]] && command -v gh >/dev/null 2>&1; then
  GH_TOKEN="$(gh auth token 2>/dev/null || true)"
fi
RH_GH_APP_ID="${RH_GH_APP_ID:-}"
RH_GH_APP_INSTALLATION_ID="${RH_GH_APP_INSTALLATION_ID:-}"
RH_GH_APP_PEM_B64="${RH_GH_APP_PEM_B64:-}"
if [[ -z "${GH_TOKEN}" && -n "${RH_GH_APP_PEM_B64}" ]]; then
  GH_TOKEN="$(scripts/mckci github app-token \
    --app-id "${RH_GH_APP_ID}" \
    --installation-id "${RH_GH_APP_INSTALLATION_ID}" \
    --pem-base64 "${RH_GH_APP_PEM_B64}")"
fi
if [[ -z "${VERSION}" || "${VERSION}" == "null" ]]; then
  echo "could not determine VERSION (pass VERSION or ensure release.json has .mongodbOperator)"
  exit 1
fi

branch="mongodb-kubernetes-${VERSION}"
bundle_file="mck-operator-certified-${VERSION}.tgz"

# Each mongodb-forks repo and its upstream owner ("repo owner"). The same bundle is published
# to both. A plain indexed array is used for portability (macOS ships bash 3.2, which has no
# associative arrays).
REPO_UPSTREAMS=(
  "certified-operators redhat-openshift-ecosystem"
  "community-operators k8s-operatorhub"
)

# On a dry run we keep the workdir so the prepared checkouts/branches can be inspected;
# on a real run we clean up. Override with KEEP_WORKDIR=true/false.
workdir="$(mktemp -d)"
echo "Workdir: ${workdir}"
if [[ "${RH_DRYRUN}" == "false" ]]; then
  KEEP_WORKDIR="${KEEP_WORKDIR:-false}"
else
  KEEP_WORKDIR="${KEEP_WORKDIR:-true}"
fi
cleanup() {
  if [[ "${KEEP_WORKDIR}" == "false" ]]; then
    rm -rf "${workdir}"
  else
    echo "Workdir kept for inspection: ${workdir}"
  fi
}
trap cleanup EXIT

echo "Downloading ${bundle_file} from ${S3_BUNDLES_URL}"
curl -fsSL "${S3_BUNDLES_URL}/${bundle_file}" -o "${workdir}/${bundle_file}"

# The tarball contains ./bundle/<version>/{bundle.Dockerfile,manifests,metadata,...}.
tar -xzf "${workdir}/${bundle_file}" -C "${workdir}"
bundle_src="${workdir}/bundle/${VERSION}"
if [[ ! -d "${bundle_src}" ]]; then
  echo "unexpected tarball layout: ${bundle_src} not found after extracting ${bundle_file}"
  exit 1
fi

# Prepares (and, unless dry-run, pushes) the branch for a single fork.
publish_repo() {
  local repo="$1" owner="$2"
  local repo_dir="${workdir}/${repo}"
  local clone_url

  if [[ -n "${GH_TOKEN}" ]]; then
    clone_url="https://x-access-token:${GH_TOKEN}@github.com/mongodb-forks/${repo}.git"
  else
    # No explicit token: rely on ambient git credentials (credential helper / cached login).
    clone_url="https://github.com/mongodb-forks/${repo}.git"
  fi
  git clone "${clone_url}" "${repo_dir}"
  git -C "${repo_dir}" remote add upstream "https://github.com/${owner}/${repo}.git"
  git -C "${repo_dir}" fetch upstream
  git -C "${repo_dir}" checkout -B "${branch}" upstream/main

  # Lay the bundle into the RedHat repo layout: operators/<package>/<version>/.
  local dest="${repo_dir}/operators/${PACKAGE}/${VERSION}"
  mkdir -p "$(dirname "${dest}")"
  rm -rf "${dest}"
  cp -r "${bundle_src}" "${dest}"

  git -C "${repo_dir}" add "operators/${PACKAGE}/${VERSION}"
  git -C "${repo_dir}" commit --quiet --signoff -m "operator ${PACKAGE} (${VERSION})"

  if [[ "${RH_DRYRUN}" == "false" ]]; then
    git -C "${repo_dir}" push -fu origin "${branch}"
  else
    echo "RH_DRYRUN=${RH_DRYRUN}: skipping push (branch prepared locally at ${repo_dir})"
  fi
}

pr_urls=()
failed=()
for entry in "${REPO_UPSTREAMS[@]}"; do
  # shellcheck disable=SC2086
  set -- ${entry}
  repo="$1"
  owner="$2"

  echo "=== ${repo} (upstream: ${owner}/${repo}) ==="
  # Attempt every repo even if a previous one failed, so one broken fork does not hide the rest.
  if publish_repo "${repo}" "${owner}"; then
    # Cross-fork compare link: opens the upstream PR form pre-filled from our fork branch.
    pr_urls+=("https://github.com/${owner}/${repo}/compare/main...mongodb-forks:${repo}:${branch}?expand=1")
  else
    echo "FAILED preparing ${repo} (see error above)"
    failed+=("${repo}")
  fi
done

echo
if [[ ${#pr_urls[@]} -gt 0 ]]; then
  echo "Release branches prepared (RH_DRYRUN=${RH_DRYRUN})."
  echo "Click the links below to open the upstream PR (mark it as a DRAFT), then ask the team"
  echo "for a quick look; certified merges on green CI, community may need a close/re-open:"
  for url in "${pr_urls[@]}"; do
    echo "  ${url}"
  done
fi

if [[ ${#failed[@]} -gt 0 ]]; then
  echo
  echo "The following repos failed: ${failed[*]}"
  exit 1
fi
