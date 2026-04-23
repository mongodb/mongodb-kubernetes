#!/bin/bash
# Build all images needed for Monarch testing with :monarch tag
#
# Usage: ./build-monarch-images.sh [--agent-path=PATH] [--om-path=PATH] [--force]
#
# Builds agent and ops-manager from source by default using:
#   ~/projects/mms-automation
#   ~/projects/ops-manager
#
# Monarch-injector uses the released binary (local builds not supported).
#
# Images pushed to staging ECR with :monarch tag:
#   - mongodb-agent:monarch
#   - mongodb-enterprise-ops-manager-ubi:monarch
#   - mongodb-kubernetes-monarch-injector:monarch
#
# The script tracks git commits and skips rebuilds if source hasn't changed.
# Use --force to rebuild all images regardless of changes.
# Builds run in parallel for faster completion.

set -e
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "${SCRIPT_DIR}/../.." && pwd)"
CACHE_DIR="${PROJECT_DIR}/.build-cache"
LOG_DIR="${PROJECT_DIR}/.build-cache/logs"

AGENT_PATH="${AGENT_PATH:-$HOME/projects/mms-automation}"
OM_PATH="${OM_PATH:-$HOME/projects/ops-manager}"
PLATFORM="${DOCKER_PLATFORM:-linux/amd64}"
FORCE=false

while [[ $# -gt 0 ]]; do
    case $1 in
        --agent-path=*) AGENT_PATH="${1#*=}"; shift ;;
        --om-path=*) OM_PATH="${1#*=}"; shift ;;
        --platform=*) PLATFORM="${1#*=}"; shift ;;
        --force|-f) FORCE=true; shift ;;
        -h|--help)
            head -19 "$0" | tail -n +2 | sed 's/^# //' | sed 's/^#//'
            exit 0
            ;;
        *) echo "Unknown option: $1"; exit 1 ;;
    esac
done

mkdir -p "${CACHE_DIR}" "${LOG_DIR}"

# Get git commit hash for a repo path
get_commit() {
    local path="$1"
    if [[ -d "$path/.git" ]]; then
        git -C "$path" rev-parse HEAD 2>/dev/null || echo "unknown"
    else
        echo "not-a-repo"
    fi
}

# Check if rebuild is needed by comparing current commit to cached commit
needs_rebuild() {
    local name="$1"
    local current_commit="$2"
    local cache_file="${CACHE_DIR}/${name}.commit"

    if [[ "$FORCE" == "true" ]]; then
        return 0  # force rebuild
    fi

    if [[ ! -f "$cache_file" ]]; then
        return 0  # no cache, need rebuild
    fi

    local cached_commit
    cached_commit=$(cat "$cache_file")
    if [[ "$cached_commit" != "$current_commit" ]]; then
        return 0  # commit changed, need rebuild
    fi

    return 1  # no rebuild needed
}

# Save commit hash to cache after successful build
save_commit() {
    local name="$1"
    local commit="$2"
    echo "$commit" > "${CACHE_DIR}/${name}.commit"
}

# Build function that runs pipeline and saves commit on success
build_image() {
    local name="$1"
    local commit="$2"
    local log_file="${LOG_DIR}/${name}.log"
    shift 2

    echo "[${name}] Starting build... (log: ${log_file})"
    if "$@" > "${log_file}" 2>&1; then
        save_commit "$name" "$commit"
        echo "[${name}] Build complete"
        return 0
    else
        echo "[${name}] Build FAILED - see ${log_file}"
        return 1
    fi
}

cd "${PROJECT_DIR}"

echo "============================================"
echo "Building Monarch test images (parallel)"
echo "============================================"
echo "Platform: ${PLATFORM}"
echo "Tag: monarch"
echo "Force rebuild: ${FORCE}"
echo "============================================"
echo ""

# Get current commits
AGENT_COMMIT=$(get_commit "$AGENT_PATH")
OM_COMMIT=$(get_commit "$OM_PATH")
MONARCH_DOCKERFILE_HASH=$(md5sum docker/monarch-injector/Dockerfile 2>/dev/null | cut -d' ' -f1 || md5 -q docker/monarch-injector/Dockerfile)

echo "Source commits:"
echo "  Agent (${AGENT_PATH}): ${AGENT_COMMIT:0:12}"
echo "  OM (${OM_PATH}): ${OM_COMMIT:0:12}"
echo "  Monarch Dockerfile: ${MONARCH_DOCKERFILE_HASH:0:12}"
echo ""

# Track background jobs
declare -a PIDS=()
declare -a NAMES=()
FAILED=false

# Build agent (background)
if needs_rebuild "agent" "$AGENT_COMMIT"; then
    build_image "agent" "$AGENT_COMMIT" \
        python scripts/release/pipeline.py agent -b staging --agent-path="$AGENT_PATH" --version monarch -p "$PLATFORM" &
    PIDS+=($!)
    NAMES+=("agent")
else
    echo "[agent] Skipping (no changes since last build)"
fi

# Build ops-manager (background)
if needs_rebuild "ops-manager" "$OM_COMMIT"; then
    build_image "ops-manager" "$OM_COMMIT" \
        python scripts/release/pipeline.py ops-manager -b staging --om-path="$OM_PATH" --version monarch -p "$PLATFORM" &
    PIDS+=($!)
    NAMES+=("ops-manager")
else
    echo "[ops-manager] Skipping (no changes since last build)"
fi

# Build monarch-injector (background)
if needs_rebuild "monarch-injector" "$MONARCH_DOCKERFILE_HASH"; then
    build_image "monarch-injector" "$MONARCH_DOCKERFILE_HASH" \
        python scripts/release/pipeline.py monarch-injector -b staging --version monarch -p "$PLATFORM" &
    PIDS+=($!)
    NAMES+=("monarch-injector")
else
    echo "[monarch-injector] Skipping (no changes since last build)"
fi

# Wait for all background jobs
echo ""
if [[ ${#PIDS[@]} -gt 0 ]]; then
    echo "Waiting for ${#PIDS[@]} build(s) to complete..."
    for i in "${!PIDS[@]}"; do
        if ! wait "${PIDS[$i]}"; then
            echo "ERROR: ${NAMES[$i]} build failed"
            FAILED=true
        fi
    done
fi

if [[ "$FAILED" == "true" ]]; then
    echo ""
    echo "============================================"
    echo "BUILD FAILED - check logs in ${LOG_DIR}"
    echo "============================================"
    exit 1
fi

echo ""
echo "============================================"
echo "Done! Images available in staging ECR:"
echo "  - mongodb-agent:monarch"
echo "  - mongodb-enterprise-ops-manager-ubi:monarch"
echo "  - mongodb-kubernetes-monarch-injector:monarch"
echo ""
echo "To use these images, switch context with monarch override:"
echo "  make switch context=e2e_static_om80_kind_ubi additional_override=private-context-monarch"
echo "============================================"

# Create context override file (must be named private-context-* for switch_context.sh)
OVERRIDE_FILE="${PROJECT_DIR}/scripts/dev/contexts/private-context-monarch"
cat > "${OVERRIDE_FILE}" << 'EOF'
# Monarch testing image overrides
# Generated by scripts/dev/build-monarch-images.sh
_MONARCH_ECR="268558157000.dkr.ecr.us-east-1.amazonaws.com/staging"
export MDB_AGENT_IMAGE="${_MONARCH_ECR}/mongodb-agent:monarch"
export MDB_OM_IMAGE="${_MONARCH_ECR}/mongodb-enterprise-ops-manager-ubi:monarch"
export MDB_MONARCH_IMAGE="${_MONARCH_ECR}/mongodb-kubernetes-monarch-injector:monarch"
EOF
echo "Created context override: scripts/dev/contexts/private-context-monarch"

echo ""
echo "Switching context..."
make -C "${PROJECT_DIR}" switch context=e2e_static_om80_kind_ubi additional_override=private-context-monarch
