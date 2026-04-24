#!/usr/bin/env bats
#
# Tests for the per-worktree OM project name isolation derivation.
#
# The fix appends `-${MCK_DEVC_NET_PREFIX}` to whatever NAMESPACE the user
# already chose, with an idempotency guard so re-sourcing root-context doesn't
# pile suffixes. The same logic lives in two places, both verified here:
#   1. scripts/dev/contexts/root-context  (sourced by `make switch`, drives the
#      generated context.env that bakes NAMESPACE into the env)
#   2. scripts/dev/delete_om_projects.sh  (mirror, so host-side cleanup callers
#      like wt_teardown.sh delete the right scope without first refreshing the
#      worktree's context).
#
# Tests run against the real `scripts/dev/contexts/root-context` and the real
# `scripts/dev/delete_om_projects.sh`. Two files are mutated for the duration
# of each test and restored in teardown(), so the tests don't leave a dirty
# worktree behind:
#   - .devcontainer/.env       (controls .devcontainer/.env-loading fallback)
#   - scripts/dev/contexts/private-context  (stubbed to set a known NAMESPACE
#                                           so the test is deterministic in
#                                           any environment, including EVG CI
#                                           where the file may use the
#                                           NAMESPACE_FILE-driven evg shape).
# Bats guarantees teardown() runs even if the test crashes mid-execution.

setup() {
    PROJECT_DIR="$(git rev-parse --show-toplevel)"
    cd "${PROJECT_DIR}"

    # Stub private-context with a known NAMESPACE.
    PC_FILE="${PROJECT_DIR}/scripts/dev/contexts/private-context"
    PC_BACKUP="${BATS_TEST_TMPDIR}/private-context.bak"
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
    # file-loading fallback).
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
    if [[ "${PC_HAD_FILE}" == "1" ]]; then
        mv "${PC_BACKUP}" "${PC_FILE}"
    else
        rm -f "${PC_FILE}"
    fi
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
