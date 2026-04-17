#!/usr/bin/env bash
# Check whether the OpenShift cluster version matches the one recorded in
# kubernetes-versions.json.  When they differ and no update-PR already exists,
# create one automatically.
#
# Usage:
#   check-ocp-version-drift.sh [--dry-run] [--actual-version X.Y]
#
#   --dry-run              Print what would happen but make no changes.
#                          Does not require gh, oc, or GH_TOKEN.
#
#   --actual-version X.Y   Override the version read from the cluster.
#                          Useful for testing without a live OCP cluster.
#                          Example: --actual-version 4.21
#
# Prerequisites (normal mode): oc (already logged in), jq, gh (authenticated via GH_TOKEN)
# Prerequisites (dry-run + --actual-version): jq

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
CONFIG_FILE="${CONFIG_FILE:-${PROJECT_ROOT}/kubernetes-versions.json}"

# --- argument parsing --------------------------------------------------------

DRY_RUN=0
ACTUAL_VERSION_OVERRIDE=""

while [[ $# -gt 0 ]]; do
    case "$1" in
        --dry-run)
            DRY_RUN=1
            shift
            ;;
        --actual-version)
            [[ $# -ge 2 ]] || { echo "check-ocp-version-drift: error: --actual-version requires an argument" >&2; exit 1; }
            ACTUAL_VERSION_OVERRIDE="$2"
            shift 2
            ;;
        *)
            echo "check-ocp-version-drift: error: unknown argument: $1" >&2
            echo "Usage: $0 [--dry-run] [--actual-version X.Y]" >&2
            exit 1
            ;;
    esac
done

# --- helpers -----------------------------------------------------------------

die() { echo "check-ocp-version-drift: error: $*" >&2; exit 1; }
log() { echo "check-ocp-version-drift: $*"; }

check_dependencies() {
    local missing=()
    command -v jq >/dev/null 2>&1 || missing+=(jq)
    if [[ -z "${ACTUAL_VERSION_OVERRIDE}" ]]; then
        command -v oc >/dev/null 2>&1 || missing+=(oc)
    fi
    if [[ "${DRY_RUN}" -eq 0 ]]; then
        command -v gh >/dev/null 2>&1 || missing+=(gh)
    fi
    if (( ${#missing[@]} > 0 )); then
        die "missing required tools: ${missing[*]}"
    fi
}

get_expected_minor() {
    local ver
    ver=$(jq -r '.openshift // empty' "${CONFIG_FILE}") || die "jq failed on ${CONFIG_FILE}"
    [[ -n "${ver}" && "${ver}" != "null" ]] || die "missing .openshift in ${CONFIG_FILE}"
    echo "${ver}" | cut -d. -f1,2
}

get_actual_minor() {
    if [[ -n "${ACTUAL_VERSION_OVERRIDE}" ]]; then
        echo "${ACTUAL_VERSION_OVERRIDE}" | cut -d. -f1,2
        return
    fi
    local full_ver
    full_ver=$(oc version -o json | jq -r '.openshiftVersion') \
        || die "could not retrieve OpenShift version from cluster"
    [[ -n "${full_ver}" && "${full_ver}" != "null" ]] \
        || die "oc version returned empty openshiftVersion"
    echo "${full_ver}" | cut -d. -f1,2
}

find_open_drift_pr() {
    # Returns "<number>\t<title>\t<url>" for the first matching open PR, or empty.
    local raw
    raw=$(gh pr list --state open --limit 100 --json number,title,url) \
        || die "gh pr list failed"
    echo "${raw}" | jq -r \
        '.[] | select(.title | test("openshift.*version|ocp.*version|update.*ocp|update.*openshift"; "i")) | "\(.number)\t\(.title)\t\(.url)"' \
        | head -1
}

create_update_pr() {
    local new_minor="$1"
    local branch="update-ocp-version-to-${new_minor}"

    if [[ "${DRY_RUN}" -eq 1 ]]; then
        log "[dry-run] would create branch '${branch}', update ${CONFIG_FILE}, and open a PR"
        return
    fi

    log "creating update PR for OpenShift ${new_minor}"
    cd "${PROJECT_ROOT}"

    git checkout -b "${branch}"

    local tmp
    tmp=$(mktemp)
    jq --arg v "${new_minor}" '.openshift = $v' "${CONFIG_FILE}" > "${tmp}"
    mv "${tmp}" "${CONFIG_FILE}"

    git add "${CONFIG_FILE}"
    git commit -m "Update OpenShift version to ${new_minor} in kubernetes-versions.json

The OpenShift cluster now runs ${new_minor} but kubernetes-versions.json still
recorded the old minor version.  This automated commit keeps the file in sync."

    git push origin "${branch}"

    gh pr create \
        --title "Update OpenShift version to ${new_minor} in kubernetes-versions.json" \
        --body "$(cat <<EOF
## Summary

The OpenShift cluster in CI is running **${new_minor}**, but \`kubernetes-versions.json\`
recorded a different minor version.  This PR updates the file to match reality.

> This PR was created automatically by \`scripts/check-ocp-version-drift.sh\`.

## Test plan

- [ ] Verify the OpenShift CI tasks pass with the updated version.
EOF
        )" \
        --base master \
        --head "${branch}"
}

# --- main --------------------------------------------------------------------

[[ "${DRY_RUN}" -eq 1 ]] && log "[dry-run mode]"
[[ -n "${ACTUAL_VERSION_OVERRIDE}" ]] && log "[actual-version override: ${ACTUAL_VERSION_OVERRIDE}]"

check_dependencies

[[ -f "${CONFIG_FILE}" ]] || die "config file not found: ${CONFIG_FILE}"

expected_minor=$(get_expected_minor)
actual_minor=$(get_actual_minor)

log "expected=${expected_minor}  actual=${actual_minor}"

if [[ "${actual_minor}" == "${expected_minor}" ]]; then
    log "versions match — nothing to do"
    exit 0
fi

log "version mismatch detected (cluster=${actual_minor}, file=${expected_minor})"

if [[ "${DRY_RUN}" -eq 0 ]]; then
    pr_line=$(find_open_drift_pr)
    if [[ -n "${pr_line}" ]]; then
        IFS=$'\t' read -r pr_num pr_title pr_url <<<"${pr_line}"
        log "update PR already open — skip"
        log "  #${pr_num}: ${pr_title}"
        log "  ${pr_url}"
        exit 0
    fi
fi

create_update_pr "${actual_minor}"
