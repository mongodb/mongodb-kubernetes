#!/usr/bin/env bash
# Verify a promoted commit+version pair; emits commit=<sha> version=<ver> on stdout.
# Usage: verify_release_commit.sh --commit <sha> --version <ver> [--staging-repo <repo>]
# All errors (with explanations) go to stderr; exit code 0 on success.

set -Eeou pipefail

staging_repo="${MCK_STAGING_REPO:-quay.io/mongodb/staging/mongodb-kubernetes}"
commit_sha=""
input_version=""

usage() { echo "Usage: $(basename "$0") --commit <sha> --version <ver> [--staging-repo <repo>]" >&2; exit "${1:-1}"; }

while [[ $# -gt 0 ]]; do
  case "$1" in
    --commit=*) commit_sha="${1#*=}" ;;
    --commit) commit_sha="$2"; shift ;;
    --version=*) input_version="${1#*=}" ;;
    --version) input_version="$2"; shift ;;
    --staging-repo=*) staging_repo="${1#*=}" ;;
    --staging-repo) staging_repo="$2"; shift ;;
    --help|-h) usage 0 ;;
    *) echo "Unknown argument: $1" >&2; usage ;;
  esac
  shift
done

: "${commit_sha:?--commit is required}"
: "${input_version:?--version is required}"

full_sha=$(git rev-parse "${commit_sha}" 2>/dev/null || true)
if [[ -z "${full_sha}" ]]; then
  echo "ERROR: could not resolve commit ${commit_sha} in the local repository. Is it a valid commit SHA and is your clone up to date (fetch-depth 0)?" >&2
  exit 1
fi

export DOCKER_CLI_EXPERIMENTAL=enabled
promoted_tag="promoted-${commit_sha}-${input_version}"
if ! docker manifest inspect "${staging_repo}:${promoted_tag}" >/dev/null 2>&1; then
  cat >&2 <<-EOF
	ERROR: commit "${commit_sha}" is not promoted, please check if the merge can be fixed or the promotion can be enforced manually:
	https://spruce.corp.mongodb.com/version/mongodb_kubernetes_${full_sha}/tasks
	This usually means the commit was not promoted, most likely due to failed or flaky tests at merge time.
	EOF
  exit 1
fi

release_json_version=$(git show "${commit_sha}:release.json" 2>/dev/null | jq -r '.mongodbOperator' 2>/dev/null || true)
if [[ -z "${release_json_version}" ]]; then
  echo "ERROR: could not read release.json at commit ${commit_sha}: is the commit valid and is your local clone up to date (fetch-depth 0)?" >&2
  exit 1
fi

if [[ "${release_json_version}" != "${input_version}" ]]; then
  echo "ERROR: release.json at commit ${commit_sha} declares mongodbOperator=${release_json_version} but requested version is ${input_version}" >&2
  exit 1
fi

echo "commit=${commit_sha}"
echo "version=${input_version}"

