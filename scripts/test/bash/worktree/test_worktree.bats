#!/usr/bin/env bats
#
# Integration tests for worktree scripts. No mocking — runs the real scripts
# against the actual clone (private-context, .generated, and bin must be present).
#
# Requirements: bats-core  (brew install bats-core)
# Run via: make test-bash    (or: scripts/test/bash/run.sh worktree)

setup() {
    export PROJECT_DIR
    PROJECT_DIR="$(git rev-parse --show-toplevel)"
    # MAIN_REPO is the original clone — equals PROJECT_DIR unless we're running
    # the tests from inside a linked worktree.
    MAIN_REPO="$(cd "$(git rev-parse --git-common-dir)/.." && pwd)"

    TEST_BRANCH="test/worktree-autotest-$$"
    BRANCH_DIR="${TEST_BRANCH//\//_}"
    PARENT_DIR="$(cd "${PROJECT_DIR}/.." && pwd)"
    WORKTREE_PATH="${PARENT_DIR}/${BRANCH_DIR}"

    # Hooks always live in the main clone's .git/hooks/, even when fired from
    # a linked worktree.
    HOOK_PATH="${MAIN_REPO}/.git/hooks/post-checkout"
    if [[ -e "${HOOK_PATH}" ]]; then
        HOOK_WAS_PRESENT=true
    else
        HOOK_WAS_PRESENT=false
    fi
}

teardown() {
    git -C "${PROJECT_DIR}" worktree remove --force "${WORKTREE_PATH}" 2>/dev/null || true
    git -C "${PROJECT_DIR}" branch -D "${TEST_BRANCH}" 2>/dev/null || true

    if [[ "${HOOK_WAS_PRESENT}" == "false" ]]; then
        rm -f "${HOOK_PATH}"
    fi
}

# Skip hook tests that delegate to init_worktree.sh in the main clone if that
# script isn't there yet (e.g. running the test from a worktree on this branch
# while master predates this change).
require_init_in_main_repo() {
    [[ -f "${MAIN_REPO}/scripts/dev/init_worktree.sh" ]] \
        || skip "init_worktree.sh not present in main clone (${MAIN_REPO}); merge this branch first or run from main clone"
}

# ---------------------------------------------------------------------------
# helpers
# ---------------------------------------------------------------------------

install_hook() {
    ln -sf "${MAIN_REPO}/.githooks/post-checkout" "${HOOK_PATH}"
}

# ---------------------------------------------------------------------------
# init_worktree.sh
# ---------------------------------------------------------------------------

@test "init_worktree: copies private-context, .generated, bin and creates venv" {
    git -C "${PROJECT_DIR}" worktree add -b "${TEST_BRANCH}" "${WORKTREE_PATH}"

    run "${PROJECT_DIR}/scripts/dev/init_worktree.sh" "${WORKTREE_PATH}" "${PROJECT_DIR}"

    [ "$status" -eq 0 ]
    [[ -f "${WORKTREE_PATH}/scripts/dev/contexts/private-context" ]]
    [[ -d "${WORKTREE_PATH}/.generated" ]]
    [[ -f "${WORKTREE_PATH}/.generated/.current_context" ]]
    [[ -d "${WORKTREE_PATH}/bin" ]]
    [[ -d "${WORKTREE_PATH}/venv" ]]
}

@test "init_worktree: idempotent — second run skips already-present files" {
    git -C "${PROJECT_DIR}" worktree add -b "${TEST_BRANCH}" "${WORKTREE_PATH}"
    "${PROJECT_DIR}/scripts/dev/init_worktree.sh" "${WORKTREE_PATH}" "${PROJECT_DIR}"

    run "${PROJECT_DIR}/scripts/dev/init_worktree.sh" "${WORKTREE_PATH}" "${PROJECT_DIR}"

    [ "$status" -eq 0 ]
    [[ "$output" == *"already exists, skipping"* ]]
}

@test "init_worktree -f: overwrites existing files" {
    git -C "${PROJECT_DIR}" worktree add -b "${TEST_BRANCH}" "${WORKTREE_PATH}"
    "${PROJECT_DIR}/scripts/dev/init_worktree.sh" "${WORKTREE_PATH}" "${PROJECT_DIR}"
    echo "corrupted" > "${WORKTREE_PATH}/scripts/dev/contexts/private-context"

    run "${PROJECT_DIR}/scripts/dev/init_worktree.sh" -f "${WORKTREE_PATH}" "${PROJECT_DIR}"

    [ "$status" -eq 0 ]
    [[ "$(cat "${WORKTREE_PATH}/scripts/dev/contexts/private-context")" != "corrupted" ]]
}

# ---------------------------------------------------------------------------
# create_worktree.sh
# ---------------------------------------------------------------------------

@test "create_worktree: places worktree in parent dir with slashes converted to underscores" {
    run env PROJECT_DIR="${PROJECT_DIR}" "${PROJECT_DIR}/scripts/dev/create_worktree.sh" "${TEST_BRANCH}"

    [ "$status" -eq 0 ]
    [[ -d "${WORKTREE_PATH}" ]]
}

@test "create_worktree: creates branch when it does not exist yet" {
    run env PROJECT_DIR="${PROJECT_DIR}" "${PROJECT_DIR}/scripts/dev/create_worktree.sh" "${TEST_BRANCH}"

    [ "$status" -eq 0 ]
    git -C "${PROJECT_DIR}" show-ref --verify --quiet "refs/heads/${TEST_BRANCH}"
}

@test "create_worktree: worktree is fully initialized after creation" {
    run env PROJECT_DIR="${PROJECT_DIR}" "${PROJECT_DIR}/scripts/dev/create_worktree.sh" "${TEST_BRANCH}"

    [ "$status" -eq 0 ]
    [[ -f "${WORKTREE_PATH}/scripts/dev/contexts/private-context" ]]
    [[ -d "${WORKTREE_PATH}/.generated" ]]
    [[ -d "${WORKTREE_PATH}/bin" ]]
    [[ -d "${WORKTREE_PATH}/venv" ]]
}

@test "create_worktree: uses existing local branch without error" {
    git -C "${PROJECT_DIR}" branch "${TEST_BRANCH}"

    run env PROJECT_DIR="${PROJECT_DIR}" "${PROJECT_DIR}/scripts/dev/create_worktree.sh" "${TEST_BRANCH}"

    [ "$status" -eq 0 ]
    [[ -d "${WORKTREE_PATH}" ]]
}

@test "create_worktree -f: re-initializes an already-initialized worktree" {
    env PROJECT_DIR="${PROJECT_DIR}" "${PROJECT_DIR}/scripts/dev/create_worktree.sh" "${TEST_BRANCH}"
    echo "corrupted" > "${WORKTREE_PATH}/scripts/dev/contexts/private-context"

    run env PROJECT_DIR="${PROJECT_DIR}" "${PROJECT_DIR}/scripts/dev/create_worktree.sh" -f "${TEST_BRANCH}"

    [ "$status" -eq 0 ]
    [[ "$(cat "${WORKTREE_PATH}/scripts/dev/contexts/private-context")" != "corrupted" ]]
}

# ---------------------------------------------------------------------------
# post-checkout hook
# ---------------------------------------------------------------------------

@test "post-checkout hook: initializes worktree automatically on git worktree add" {
    require_init_in_main_repo
    install_hook
    git -C "${PROJECT_DIR}" worktree add -b "${TEST_BRANCH}" "${WORKTREE_PATH}"

    [[ -f "${WORKTREE_PATH}/scripts/dev/contexts/private-context" ]]
    [[ -d "${WORKTREE_PATH}/venv" ]]
}

@test "post-checkout hook: does not fire during rebase" {
    # Create a bare worktree without init (hook not installed yet).
    git -C "${PROJECT_DIR}" worktree add -b "${TEST_BRANCH}" "${WORKTREE_PATH}"

    # Simulate an in-progress rebase inside that worktree.
    git_dir="$(git -C "${WORKTREE_PATH}" rev-parse --git-dir)"
    mkdir -p "${git_dir}/rebase-merge"

    # Run the hook directly — it should exit early without initializing.
    cd "${WORKTREE_PATH}"
    run "${PROJECT_DIR}/.githooks/post-checkout" HEAD HEAD 1

    [ "$status" -eq 0 ]
    [[ ! -d "${WORKTREE_PATH}/venv" ]]

    rm -rf "${git_dir}/rebase-merge"
}

@test "post-checkout hook: does not fire in the main clone" {
    # Run hook from the main clone, where .git is a directory — the guard
    # should exit early before reaching init.
    cd "${MAIN_REPO}"
    run "${PROJECT_DIR}/.githooks/post-checkout" HEAD HEAD 1

    [ "$status" -eq 0 ]
}
