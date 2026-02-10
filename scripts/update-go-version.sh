#!/bin/bash
# Updates Go version across all files in the codebase.
# Source of truth: go.mod in the root directory.
#
# Usage: ./scripts/update-go-version.sh
#
# When adding new files that reference Go version, add them to the
# appropriate array below.

set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"

# go.mod files use full version with "go X.XX.X" format
GO_MOD_FILES=(
    "docker/mongodb-kubernetes-tests/public/tools/multicluster/go.mod"
)

# .tool-versions uses full version with "golang X.XX.X" format
TOOL_VERSION_FILES=(
    ".tool-versions"
)

# Dockerfiles use minor version in image tag (e.g., "golang:1.24")
DOCKERFILE_FILES=(
    "cmd/kubectl-mongodb/Dockerfile"
    "docker/delve-sidecar/Dockerfile"
    "docker/mongodb-community-tests/Dockerfile"
    "docker/mongodb-enterprise-ops-manager/Dockerfile"
    "docker/mongodb-kubernetes-init-appdb/Dockerfile"
    "docker/mongodb-kubernetes-init-database/Dockerfile"
    "docker/mongodb-kubernetes-init-ops-manager/Dockerfile"
    "docker/mongodb-kubernetes-operator/Dockerfile"
    "docker/mongodb-kubernetes-readinessprobe/Dockerfile"
    "docker/mongodb-kubernetes-upgrade-hook/Dockerfile"
)

# Dev scripts use minor version in path (e.g., "/opt/golang/go1.24")
DEV_SCRIPT_FILES=(
    "scripts/dev/prepare_local_e2e_run.sh"
    "scripts/dev/contexts/root-context"
    "scripts/dev/contexts/evg-private-context"
)

update_files() {
    local pattern="$1"
    local new_value="$2"
    shift 2
    local files=("$@")

    for file in "${files[@]}"; do
        local filepath="${ROOT_DIR}/${file}"
        if [[ -f "${filepath}" ]]; then
            # Use a temporary file for cross-platform compatibility
            local tmpfile=$(mktemp)
            sed "s|${pattern}|${new_value}|g" "${filepath}" > "${tmpfile}"
            mv "${tmpfile}" "${filepath}"
            echo "Updated: ${file}"
        fi
    done
}

# Extract version from go.mod (source of truth)
FULL_VERSION=$(go list -m -f '{{.GoVersion}}')
if [[ -z "${FULL_VERSION}" ]]; then
    echo "Error: Could not extract Go version from go.mod"
    exit 1
fi

MINOR_VERSION="${FULL_VERSION%.*}"

echo "Go version: ${FULL_VERSION} (minor: ${MINOR_VERSION})"
echo ""

update_files "^go .*" "go ${FULL_VERSION}" "${GO_MOD_FILES[@]}"
update_files "^golang .*" "golang ${FULL_VERSION}" "${TOOL_VERSION_FILES[@]}"
update_files "golang:[0-9.]*" "golang:${MINOR_VERSION}" "${DOCKERFILE_FILES[@]}"
update_files "/opt/golang/go[0-9.]*" "/opt/golang/go${MINOR_VERSION}" "${DEV_SCRIPT_FILES[@]}"

echo ""
echo "Done. Run 'git diff' to review changes."
