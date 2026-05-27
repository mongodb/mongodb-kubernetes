#!/bin/bash
# Copyright 2026 MongoDB Inc
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.


set -euo pipefail

# Configuration
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
CONFIG_FILE="${CONFIG_FILE:-${PROJECT_ROOT}/kubernetes-versions.json}"
OPERATOR_RELEASE_DATE="${OPERATOR_RELEASE_DATE:-$(date +%Y-%m-%d)}"
BUFFER_MONTHS=1

# API Endpoints
K8S_API="https://endoflife.date/api/kubernetes.json"
OCP_API="https://endoflife.date/api/red-hat-openshift.json"

# Documentation
UPDATE_DOCS_URL="https://github.com/mongodb/mongodb-kubernetes/blob/master/docs/dev/kubernetes-version-support.md"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

# Exit code tracking
EXIT_CODE=0

# Output file for logging
OUTPUT_FILE="${OUTPUT_FILE:-$(mktemp /tmp/kube-versions-check.XXXXXX)}"
OUTPUT_FD=0  # File descriptor for output file (will be set in main)

# Main execution
main() {
    local output_file="${OUTPUT_FILE}"

    # Open output file for writing (file descriptor 5)
    exec 5>>"${output_file}"
    OUTPUT_FD=5

    info "--------------------------------------------------------"
    info "Checking Support Policy Compliance"
    display "Operator Release Date: ${OPERATOR_RELEASE_DATE}"
    display "Buffer: ${BUFFER_MONTHS} months"
    info "--------------------------------------------------------"

    check_dependencies
    validate_config

    THRESHOLD_DATE=$(calc_date "${OPERATOR_RELEASE_DATE}" "-${BUFFER_MONTHS}")
    EOL_THRESHOLD_DATE=$(calc_date "${OPERATOR_RELEASE_DATE}" "+${BUFFER_MONTHS}")

    display "Policy Cutoff (New Versions): Releases before ${THRESHOLD_DATE} must be supported."
    display ""

    # Load and validate config values
    local k8s_min k8s_max ocp_ver

    k8s_min=$(jq -r '.kubernetes.min // empty' "${CONFIG_FILE}")
    k8s_max=$(jq -r '.kubernetes.max // empty' "${CONFIG_FILE}")
    ocp_ver=$(jq -r '.openshift // empty' "${CONFIG_FILE}")

    if [ -z "${k8s_min}" ] || [ "${k8s_min}" == "null" ]; then
        error "Missing or invalid kubernetes.min in config file"
        exit 1
    fi

    if [ -z "${k8s_max}" ] || [ "${k8s_max}" == "null" ]; then
        error "Missing or invalid kubernetes.max in config file"
        exit 1
    fi

    if [ -z "${ocp_ver}" ] || [ "${ocp_ver}" == "null" ]; then
        error "Missing or invalid openshift in config file"
        exit 1
    fi

    validate_version_format "${k8s_min}" || exit 1
    validate_version_format "${k8s_max}" || exit 1
    validate_version_format "${ocp_ver}" || exit 1

    # Execute checks
    check_k8s_range "${k8s_min}" "${k8s_max}" || true
    check_ocp_single "${ocp_ver}" || true

    if [ ${EXIT_CODE} -eq 0 ]; then
        display ""
        success "All version checks passed!"
    else
        display ""
        warning "Some version checks failed or require attention"
        display ""
        display "Please update the versions following the instructions at:"
        display "${UPDATE_DOCS_URL}"
    fi

    # Close output file descriptor
    exec 5>&-
    OUTPUT_FD=0

    # Send to Slack if there are issues
    if [ ${EXIT_CODE} -ne 0 ]; then
        send_to_slack "${output_file}"
    fi

    # Cleanup output file if it was a temp file
    if [[ "${output_file}" == /tmp/kube-versions-check.* ]]; then
        rm -f "${output_file}"
    fi

    exit ${EXIT_CODE}
}

# Check dependencies
check_dependencies() {
    local missing_deps=()

    for cmd in jq curl date; do
        if ! command -v "${cmd}" >/dev/null 2>&1; then
            missing_deps+=("${cmd}")
        fi
    done

    if [ ${#missing_deps[@]} -gt 0 ]; then
        error "Missing required dependencies: ${missing_deps[*]}"
        exit 1
    fi
}

# Validate config file exists
validate_config() {
    if [ ! -f "${CONFIG_FILE}" ]; then
        error "Config file not found: ${CONFIG_FILE}"
        exit 1
    fi

    if ! jq empty "${CONFIG_FILE}" >/dev/null 2>&1; then
        error "Config file is not valid JSON: ${CONFIG_FILE}"
        exit 1
    fi
}

# Send output file to Slack if version changes are needed
send_to_slack() {
    local output_file="$1"
    local slack_script="${SCRIPT_DIR}/slackit.sh"

    if [ ! -f "${slack_script}" ]; then
        echo "Warning: slackit.sh not found at ${slack_script}, skipping Slack notification" >&2
        return 0
    fi

    if [ ! -x "${slack_script}" ]; then
        echo "Warning: slackit.sh is not executable, skipping Slack notification" >&2
        return 0
    fi

    if [ ! -f "${output_file}" ]; then
        echo "Warning: Output file not found: ${output_file}" >&2
        return 0
    fi

    # Check if SLACK_WEBHOOK is set
    if [ -z "${SLACK_WEBHOOK:-}" ]; then
        echo "Warning: SLACK_WEBHOOK not set, skipping Slack notification" >&2
        return 0
    fi

    echo "Sending version check results to Slack..." >&2
    º"${slack_script}" < "${output_file}" || {
        echo "Warning: Failed to send message to Slack" >&2
        return 0
    }
}

# Check Kubernetes range support
check_k8s_range() {
    local current_raw_min="$1"
    local current_raw_max="$2"

    # Normalize versions once at the start (API returns X.Y, config may have X.Y.Z)
    local current_min current_max
    current_min=$(normalize_version "${current_raw_min}")
    current_max=$(normalize_version "${current_raw_max}")

    info "### Checking Kubernetes Range (${current_min} - ${current_max})"

    local data
    data=$(fetch_api_data "${K8S_API}" "Kubernetes") || return 1

    # Check Max Version (Latest Buffered)
    local target_max
    if ! target_max=$(find_target_version "${data}" "${THRESHOLD_DATE}"); then
        error "Could not determine target max Kubernetes version"
        return 1
    fi

    if [ "${current_max}" == "${target_max}" ]; then
        success "Max version is correct (${current_max})"
    else
        local newer
        newer=$(compare_versions "${current_max}" "${target_max}")

        if [ "${newer}" == "${target_max}" ] && [ "${current_max}" != "${target_max}" ]; then
            warning "UPDATE REQUIRED: Max version should be bumped to ${target_max} (current: ${current_max})"
        elif [ "${newer}" == "${current_max}" ]; then
            success "You are ahead of policy (${current_max} > ${target_max}). OK"
        fi
    fi

    # Check Min Version (EOL Risk)
    local min_eol
    min_eol=$(echo "${data}" | jq -r --arg ver "${current_min}" '.[] | select(.cycle == $ver) | .eol // empty')

    if [ -z "${min_eol}" ] || [ "${min_eol}" == "null" ] || [ "${min_eol}" == "false" ]; then
        success "Min version ${current_raw_min} has no EOL date set"
    else
        # Convert dates to epoch for proper comparison
        local eol_epoch min_eol_epoch
        if date -j -f "%Y-%m-%d" "${EOL_THRESHOLD_DATE}" >/dev/null 2>&1; then
            # BSD date
            eol_epoch=$(date -j -f "%Y-%m-%d" "${EOL_THRESHOLD_DATE}" +%s 2>/dev/null || echo "0")
            min_eol_epoch=$(date -j -f "%Y-%m-%d" "${min_eol}" +%s 2>/dev/null || echo "0")
        else
            # GNU date
            eol_epoch=$(date -d "${EOL_THRESHOLD_DATE}" +%s 2>/dev/null || echo "0")
            min_eol_epoch=$(date -d "${min_eol}" +%s 2>/dev/null || echo "0")
        fi

        if [ "${min_eol_epoch}" -gt 0 ] && [ "${eol_epoch}" -gt 0 ] && [ "${min_eol_epoch}" -lt "${eol_epoch}" ]; then
            warning "EOL WARNING: Version ${current_min} EOL is ${min_eol} (within buffer window). Consider bumping min"
        else
            success "Min version ${current_min} is valid (EOL: ${min_eol})"
        fi
    fi
    display ""
}

# Check OpenShift single version commitment
check_ocp_single() {
    local current_raw_ver="$1"

    # Normalize version once at the start (API returns X.Y, config may have X.Y.Z)
    local current_ver
    current_ver=$(normalize_version "${current_raw_ver}")

    info "### Checking OpenShift Version (${current_raw_ver})"

    local data
    data=$(fetch_api_data "${OCP_API}" "OpenShift") || return 1

    # Find the latest version released before the Threshold Date
    local target_ver
    if ! target_ver=$(find_target_version "${data}" "${THRESHOLD_DATE}"); then
        error "Could not determine target OpenShift version"
        return 1
    fi

    if [ "${current_ver}" == "${target_ver}" ]; then
        success "OpenShift version is correct (${current_raw_ver})"
    else
        local newer
        newer=$(compare_versions "${current_ver}" "${target_ver}")

        if [ "${newer}" == "${target_ver}" ] && [ "${current_ver}" != "${target_ver}" ]; then
            warning "UPDATE REQUIRED: Latest eligible OpenShift version is ${target_ver} (current: ${current_raw_ver})"
            display "  (Your config says ${current_raw_ver}, but ${target_ver} was released before threshold date)"
        elif [ "${newer}" == "${current_ver}" ]; then
            success "You are ahead of policy (${current_raw_ver} > ${target_ver}). This is fine"
        fi
    fi
    display ""
}

# Fetch API data with error handling
fetch_api_data() {
    local url="$1"
    local api_name="$2"
    local http_code
    local data

    http_code=$(curl -s -o /dev/null -w "%{http_code}" "${url}" || echo "000")

    if [ "${http_code}" != "200" ]; then
        error "Failed to fetch ${api_name} data (HTTP ${http_code})"
        return 1
    fi

    data=$(curl -s "${url}")

    if [ -z "${data}" ] || [ "${data}" == "null" ]; then
        error "Empty or null response from ${api_name} API"
        return 1
    fi

    if ! echo "${data}" | jq empty >/dev/null 2>&1; then
        error "Invalid JSON response from ${api_name} API"
        return 1
    fi

    echo "${data}"
}

# Find latest version released before threshold date
find_target_version() {
    local data="$1"
    local threshold_date="$2"

    local target_ver
    target_ver=$(echo "${data}" | jq -r --arg date "${threshold_date}" '
        map(select(.cycle | test("^[0-9]+\\.[0-9]+$"))) |
        map(select(.releaseDate <= $date)) |
        sort_by(.releaseDate) |
        last |
        .cycle // empty')

    if [ -z "${target_ver}" ] || [ "${target_ver}" == "null" ]; then
        return 1
    fi

    echo "${target_ver}"
}

# Compare two version strings
compare_versions() {
    local ver1="$1"
    local ver2="$2"
    echo -e "${ver1}\n${ver2}" | sort -V | tail -n1
}

# Validate version format (X.Y or X.Y.Z)
validate_version_format() {
    local version="$1"
    if [[ ! "${version}" =~ ^[0-9]+\.[0-9]+(\.[0-9]+)?$ ]]; then
        error "Invalid version format: ${version} (expected X.Y or X.Y.Z)"
        return 1
    fi
}

# Normalize version to X.Y format (strip patch version if present)
# This is used when comparing against API data which only provides X.Y format
normalize_version() {
    local version="$1"
    echo "${version}" | cut -d. -f1,2
}

# Calculate date with month offset
calc_date() {
    local date_str="$1"
    local months="$2"

    # Detect BSD (macOS) vs GNU date
    if date -v+1m >/dev/null 2>&1; then
        # BSD date (macOS)
        if [[ ${months} =~ ^- ]]; then
            # Negative months: remove minus sign and use -v-${abs}m
            local abs_months="${months#-}"
            date -v-"${abs_months}m" -j -f "%Y-%m-%d" "${date_str}" +%Y-%m-%d
        elif [[ ${months} =~ ^\+ ]]; then
            # Positive months: remove plus sign and use -v+${abs}m
            local abs_months="${months#+}"
            date -v+"${abs_months}m" -j -f "%Y-%m-%d" "${date_str}" +%Y-%m-%d
        else
            # No sign prefix, assume positive
            date -v+"${months}m" -j -f "%Y-%m-%d" "${date_str}" +%Y-%m-%d
        fi
    else
        # GNU date (Linux)
        date -d "${date_str} ${months} months" +%Y-%m-%d
    fi
}

# Output helper functions
display() {
    echo "$1"
    write_to_file "$1"
}

error() {
    local msg="Error: $1"
    echo -e "${RED}${msg}${NC}" >&2
    write_to_file "${msg}"
    EXIT_CODE=1
}

warning() {
    local msg="⚠ $1"
    echo -e "${YELLOW}${msg}${NC}"
    write_to_file "${msg}"
    EXIT_CODE=1
}

info() {
    echo -e "${BLUE}$1${NC}"
    write_to_file "$1"
}

success() {
    local msg="✓ $1"
    echo -e "${GREEN}${msg}${NC}"
    write_to_file "${msg}"
}

# Helper function to write plain text to output file
write_to_file() {
    if [ ${OUTPUT_FD} -gt 0 ]; then
        echo "$1" >&${OUTPUT_FD}
    fi
}

main "$@"
