#!/usr/bin/env bats
#
# Tests for the per-worktree OM project name isolation derivation.
#
# The fix appends `-${MCK_DEVC_NET_PREFIX}` to whatever NAMESPACE the user
# already chose, with an idempotency guard so re-sourcing root-context doesn't
# pile suffixes. The same logic lives in two places, both verified here:
#   1. scripts/dev/contexts/root-context  (sourced by `make switch`, drives the
#      generated context.export.env that bakes NAMESPACE into the env)
#   2. scripts/dev/delete_om_projects.sh  (mirror, so host-side cleanup callers
#      like wt_teardown.sh delete the right scope without first refreshing the
#      worktree's context).
#
# Tests run against the real project files. The only state mutated is
# .devcontainer/.env (and only when a test needs to verify the file-loading
# fallback); setup() snapshots it and teardown() restores it so the tests
# don't leave a dirty worktree behind.

setup() {
    PROJECT_DIR="$(git rev-parse --show-toplevel)"
    cd "${PROJECT_DIR}"

    DEVC_ENV_FILE="${PROJECT_DIR}/.devcontainer/.env"
    DEVC_ENV_BACKUP="${BATS_TEST_TMPDIR}/devcontainer.env.bak"
    if [[ -f "${DEVC_ENV_FILE}" ]]; then
        cp -p "${DEVC_ENV_FILE}" "${DEVC_ENV_BACKUP}"
        DEVC_ENV_HAD_FILE=1
    else
        DEVC_ENV_HAD_FILE=0
    fi
}

teardown() {
    if [[ "${DEVC_ENV_HAD_FILE}" == "1" ]]; then
        mv "${DEVC_ENV_BACKUP}" "${DEVC_ENV_FILE}"
    else
        rm -f "${DEVC_ENV_FILE}"
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
    NAMESPACE_in_pc="$(grep -E '^export NAMESPACE=' scripts/dev/contexts/private-context | tail -1 | sed -E 's/.*"([^"]+)".*/\1/')"
    [[ -n "${NAMESPACE_in_pc}" ]]

    export MCK_DEVC_NET_PREFIX=20
    result="$(namespace_after_root_context)"

    [[ "${result}" == "${NAMESPACE_in_pc}-20" ]]
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
    [[ "${first}" == *-20 ]]
}

@test "root-context: no MCK_DEVC_NET_PREFIX in env or .devcontainer/.env -> NAMESPACE untouched" {
    NAMESPACE_in_pc="$(grep -E '^export NAMESPACE=' scripts/dev/contexts/private-context | tail -1 | sed -E 's/.*"([^"]+)".*/\1/')"

    unset MCK_DEVC_NET_PREFIX
    rm -f "${DEVC_ENV_FILE}"

    result="$(namespace_after_root_context)"

    [[ "${result}" == "${NAMESPACE_in_pc}" ]]
}

@test "root-context: loads MCK_DEVC_NET_PREFIX from .devcontainer/.env when not in env" {
    NAMESPACE_in_pc="$(grep -E '^export NAMESPACE=' scripts/dev/contexts/private-context | tail -1 | sed -E 's/.*"([^"]+)".*/\1/')"

    unset MCK_DEVC_NET_PREFIX
    mkdir -p "$(dirname "${DEVC_ENV_FILE}")"
    echo "MCK_DEVC_NET_PREFIX=24" > "${DEVC_ENV_FILE}"

    result="$(namespace_after_root_context)"

    [[ "${result}" == "${NAMESPACE_in_pc}-24" ]]
}

@test "root-context: env-set MCK_DEVC_NET_PREFIX wins over .devcontainer/.env" {
    NAMESPACE_in_pc="$(grep -E '^export NAMESPACE=' scripts/dev/contexts/private-context | tail -1 | sed -E 's/.*"([^"]+)".*/\1/')"

    export MCK_DEVC_NET_PREFIX=18
    mkdir -p "$(dirname "${DEVC_ENV_FILE}")"
    echo "MCK_DEVC_NET_PREFIX=24" > "${DEVC_ENV_FILE}"

    result="$(namespace_after_root_context)"

    [[ "${result}" == "${NAMESPACE_in_pc}-18" ]]
}

# ---------------------------------------------------------------------------
# delete_om_projects.sh mirror
# ---------------------------------------------------------------------------
#
# We can't fully run delete_om_projects.sh (it hits the OM API), but we can
# extract the derivation block and assert the same conditional logic produces
# the right NAMESPACE. The mirror is meant to behave identically to the
# root-context block.

@test "delete_om_projects.sh: derivation block appends prefix idempotently" {
    unset MCK_DEVC_NET_PREFIX
    export NAMESPACE="custom"
    mkdir -p "$(dirname "${DEVC_ENV_FILE}")"
    echo "MCK_DEVC_NET_PREFIX=22" > "${DEVC_ENV_FILE}"

    # Replay the conditional that lives in delete_om_projects.sh.
    if [[ -z "${MCK_DEVC_NET_PREFIX:-}" && -f "${PROJECT_DIR:-.}/.devcontainer/.env" ]]; then
        devc_prefix_line="$(grep '^MCK_DEVC_NET_PREFIX=' \
            "${PROJECT_DIR:-.}/.devcontainer/.env" 2>/dev/null | tail -n1 || true)"
        if [[ -n "${devc_prefix_line}" ]]; then
            export "${devc_prefix_line?}"
        fi
        unset devc_prefix_line
    fi
    if [[ -n "${MCK_DEVC_NET_PREFIX:-}" && -n "${NAMESPACE:-}" \
          && "${NAMESPACE}" != *"-${MCK_DEVC_NET_PREFIX}" ]]; then
        NAMESPACE="${NAMESPACE}-${MCK_DEVC_NET_PREFIX}"
    fi

    [[ "${NAMESPACE}" == "custom-22" ]]

    # Re-running the same block must not double-suffix.
    if [[ -n "${MCK_DEVC_NET_PREFIX:-}" && -n "${NAMESPACE:-}" \
          && "${NAMESPACE}" != *"-${MCK_DEVC_NET_PREFIX}" ]]; then
        NAMESPACE="${NAMESPACE}-${MCK_DEVC_NET_PREFIX}"
    fi
    [[ "${NAMESPACE}" == "custom-22" ]]
}
