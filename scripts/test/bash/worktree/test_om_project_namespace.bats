#!/usr/bin/env bats
#
# Per-worktree OM project isolation against two real scripts:
#   1. scripts/dev/contexts/root-context — appends `-${MCK_DEVC_NET_PREFIX}`
#      to NAMESPACE (idempotent).
#   2. scripts/dev/delete_om_projects.sh `filter_prefixed_projects` — must
#      never delete a sibling worktree stack's OM projects.
#
# Tests mutate .devcontainer/.env and scripts/dev/contexts/private-context,
# then restore in teardown(); backups live beside the originals on disk (not
# in BATS_TEST_TMPDIR) so an aborted run stays recoverable.

setup() {
    PROJECT_DIR="$(git rev-parse --show-toplevel)"
    cd "${PROJECT_DIR}"

    # Stub private-context (a gitignored, credential-bearing file) with a known
    # NAMESPACE. The backup lives BESIDE the original on disk — never in
    # BATS_TEST_TMPDIR, which bats deletes on exit, so a SIGKILL mid-run would
    # otherwise destroy the developer's real private-context unrecoverably.
    PC_FILE="${PROJECT_DIR}/scripts/dev/contexts/private-context"
    PC_BACKUP="${PC_FILE}.bats-bak"
    # Self-heal: a leftover backup from a previously aborted run is the real
    # file — restore it before we stub, so we never stack stubs or lose data.
    [[ -f "${PC_BACKUP}" ]] && mv -f "${PC_BACKUP}" "${PC_FILE}"
    if [[ -f "${PC_FILE}" ]]; then
        cp -p "${PC_FILE}" "${PC_BACKUP}"
        PC_HAD_FILE=1
    else
        PC_HAD_FILE=0
    fi
    KNOWN_NS="bats-namespace"
    cat >"${PC_FILE}" <<EOF
#!/bin/bash
export NAMESPACE="${KNOWN_NS}"
EOF

    # Snapshot .devcontainer/.env (some tests mutate it to verify the
    # file-loading fallback). Same on-disk backup discipline as above.
    DEVC_ENV_FILE="${PROJECT_DIR}/.devcontainer/.env"
    DEVC_ENV_BACKUP="${DEVC_ENV_FILE}.bats-bak"
    [[ -f "${DEVC_ENV_BACKUP}" ]] && mv -f "${DEVC_ENV_BACKUP}" "${DEVC_ENV_FILE}"
    if [[ -f "${DEVC_ENV_FILE}" ]]; then
        cp -p "${DEVC_ENV_FILE}" "${DEVC_ENV_BACKUP}"
        DEVC_ENV_HAD_FILE=1
    else
        DEVC_ENV_HAD_FILE=0
    fi
}

teardown() {
    if [[ "${PC_HAD_FILE}" == "1" ]]; then
        mv -f "${PC_BACKUP}" "${PC_FILE}"
    else
        rm -f "${PC_FILE}" "${PC_BACKUP}"
    fi
    if [[ "${DEVC_ENV_HAD_FILE}" == "1" ]]; then
        mv -f "${DEVC_ENV_BACKUP}" "${DEVC_ENV_FILE}"
    else
        rm -f "${DEVC_ENV_FILE}" "${DEVC_ENV_BACKUP}"
    fi
}

# Source root-context in a clean subshell with a controlled env, capture the
# resulting NAMESPACE. Subshell isolation prevents the source from polluting
# the bats process; redirecting stdout/stderr keeps EVG-host probing chatter
# out of the captured value.
namespace_after_root_context() {
    bash -c '
        set -e
        cd "${PROJECT_DIR}"
        # shellcheck disable=SC1091
        source scripts/dev/contexts/root-context >/dev/null 2>&1
        printf "%s" "${NAMESPACE}"
    '
}

# ---------------------------------------------------------------------------
# root-context derivation
# ---------------------------------------------------------------------------

@test "root-context: appends -PREFIX to the user's NAMESPACE" {
    export MCK_DEVC_NET_PREFIX=20
    result="$(namespace_after_root_context)"

    [[ "${result}" == "${KNOWN_NS}-20" ]]
}

@test "root-context: idempotent — already-suffixed NAMESPACE is left alone" {
    # private-context exports the un-suffixed NAMESPACE. After the first
    # source, NAMESPACE ends in -20. A second source must not append again.
    export MCK_DEVC_NET_PREFIX=20

    result="$(bash -c '
        set -e
        cd "${PROJECT_DIR}"
        # shellcheck disable=SC1091
        source scripts/dev/contexts/root-context >/dev/null 2>&1
        first="${NAMESPACE}"
        # shellcheck disable=SC1091
        source scripts/dev/contexts/root-context >/dev/null 2>&1
        printf "%s|%s" "${first}" "${NAMESPACE}"
    ')"

    first="${result%%|*}"
    second="${result#*|}"
    [[ "${first}" == "${second}" ]]
    [[ "${first}" == "${KNOWN_NS}-20" ]]
}

@test "root-context: no MCK_DEVC_NET_PREFIX in env or .devcontainer/.env -> NAMESPACE untouched" {
    unset MCK_DEVC_NET_PREFIX
    rm -f "${DEVC_ENV_FILE}"

    result="$(namespace_after_root_context)"

    [[ "${result}" == "${KNOWN_NS}" ]]
}

@test "root-context: loads MCK_DEVC_NET_PREFIX from .devcontainer/.env when not in env" {
    unset MCK_DEVC_NET_PREFIX
    mkdir -p "$(dirname "${DEVC_ENV_FILE}")"
    echo "MCK_DEVC_NET_PREFIX=24" > "${DEVC_ENV_FILE}"

    result="$(namespace_after_root_context)"

    [[ "${result}" == "${KNOWN_NS}-24" ]]
}

@test "root-context: env-set MCK_DEVC_NET_PREFIX wins over .devcontainer/.env" {
    export MCK_DEVC_NET_PREFIX=18
    mkdir -p "$(dirname "${DEVC_ENV_FILE}")"
    echo "MCK_DEVC_NET_PREFIX=24" > "${DEVC_ENV_FILE}"

    result="$(namespace_after_root_context)"

    [[ "${result}" == "${KNOWN_NS}-18" ]]
}

# ---------------------------------------------------------------------------
# delete_om_projects.sh prefix filter (worktree isolation)
# ---------------------------------------------------------------------------
#
# Sourcing the script hits its BASH_SOURCE guard, so main() (and every OM API
# call) is skipped and we can drive the pure filter with a fixed JSON payload.

# Sample OM /groups response covering: the bare base namespace, base resource-
# specific projects, and two sibling worktree stacks (numeric net-prefix).
groups_json() {
    cat <<'EOF'
{"results":[
  {"name":"mongodb-test"},
  {"name":"mongodb-test-mdb-sh"},
  {"name":"mongodb-test-mdb"},
  {"name":"mongodb-test-30"},
  {"name":"mongodb-test-30-mdb-sh"},
  {"name":"mongodb-test-42"},
  {"name":"other-project"}
]}
EOF
}

@test "delete_om_projects.sh: base prefix deletes own resource projects, spares sibling worktrees" {
    # shellcheck disable=SC1091
    source scripts/dev/delete_om_projects.sh
    run bash -c "$(declare -f filter_prefixed_projects); $(declare -f groups_json); groups_json | filter_prefixed_projects mongodb-test"
    [[ "${status}" -eq 0 ]]
    # Own resource-specific projects: deleted.
    [[ "${output}" == *"mongodb-test-mdb-sh"* ]]
    [[ "${output}" == *"mongodb-test-mdb"* ]]
    # Sibling worktree stacks and their sub-projects: never touched.
    [[ "${output}" != *"mongodb-test-30"* ]]
    [[ "${output}" != *"mongodb-test-42"* ]]
    # Non-matching org projects: never touched.
    [[ "${output}" != *"other-project"* ]]
}

@test "delete_om_projects.sh: worktree prefix cleans only its own sub-projects" {
    # shellcheck disable=SC1091
    source scripts/dev/delete_om_projects.sh
    run bash -c "$(declare -f filter_prefixed_projects); $(declare -f groups_json); groups_json | filter_prefixed_projects mongodb-test-30"
    [[ "${status}" -eq 0 ]]
    [[ "${output}" == *"mongodb-test-30-mdb-sh"* ]]
    # Must not reach up to the base namespace's resource projects or siblings.
    [[ "${output}" != *"mongodb-test-mdb-sh"* ]]
    [[ "${output}" != *"mongodb-test-42"* ]]
}
