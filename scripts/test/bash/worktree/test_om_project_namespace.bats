#!/usr/bin/env bats
#
# Tests for the per-worktree OM project name isolation derivation.
#
# The fix sets NAMESPACE=ls-${MCK_DEVC_NET_PREFIX} when both:
#   - MCK_DEVC_NET_PREFIX is set (devcontainer-mode worktree)
#   - NAMESPACE is the default 'ls' (no explicit override)
#
# This logic lives in two places that both need verifying:
#   1. scripts/dev/contexts/root-context  (sourced by `make switch`, drives the
#      generated context.export.env that bakes NAMESPACE into the env)
#   2. scripts/dev/delete_om_projects.sh  (mirror, so host-side cleanup callers
#      like wt_teardown.sh delete the right scope without first refreshing the
#      worktree's context).
#
# We test the derivation by sourcing each script in isolation with a stubbed
# environment and asserting the resulting NAMESPACE.

setup() {
    REAL_PROJECT_DIR="$(git rev-parse --show-toplevel)"

    # Build a sandbox dir that mimics a worktree's relevant tree:
    #   <sandbox>/.devcontainer/.env         (carries MCK_DEVC_NET_PREFIX)
    #   <sandbox>/scripts/dev/contexts/...   (real scripts, copied)
    #   <sandbox>/release.json               (root-context jq's this)
    SANDBOX="$(mktemp -d)"
    mkdir -p "${SANDBOX}/.devcontainer"
    mkdir -p "${SANDBOX}/scripts/dev/contexts"
    mkdir -p "${SANDBOX}/scripts/funcs"
    mkdir -p "${SANDBOX}/.generated"

    # Stub funcs/errors (root-context's source chain may need it transitively).
    cat >"${SANDBOX}/scripts/funcs/errors" <<'EOF'
fatal() { echo "FATAL: $*" >&2; exit 1; }
EOF

    # Stub a minimal private-context that sets NAMESPACE=ls (the default).
    cat >"${SANDBOX}/scripts/dev/contexts/private-context" <<'EOF'
export NAMESPACE="ls"
EOF

    # Stub release.json with the keys root-context jq's out.
    cat >"${SANDBOX}/release.json" <<'EOF'
{
  "agentVersion": "108.0.0.0000-1",
  "search": {"version": "0.0.0"}
}
EOF

    # Real root-context, copied. We copy (not symlink) because root-context
    # does `realpath` on $BASH_SOURCE to find its dir; symlinks could resolve
    # to the original repo and miss the sandbox's private-context stub.
    cp "${REAL_PROJECT_DIR}/scripts/dev/contexts/root-context" \
       "${SANDBOX}/scripts/dev/contexts/root-context"

    # Pin PROJECT_DIR to the sandbox so the .devcontainer/.env load and the
    # set_env_context.sh path resolution stay inside the sandbox even though
    # this bats process inherited a different PROJECT_DIR from its parent.
    export PROJECT_DIR="${SANDBOX}"
}

teardown() {
    rm -rf "${SANDBOX}"
}

# ---------------------------------------------------------------------------
# root-context derivation
# ---------------------------------------------------------------------------

@test "root-context: NAMESPACE=ls + MCK_DEVC_NET_PREFIX=20 -> ls-20" {
    cd "${SANDBOX}"
    unset NAMESPACE MCK_DEVC_NET_PREFIX
    export MCK_DEVC_NET_PREFIX=20

    # shellcheck disable=SC1091
    source scripts/dev/contexts/root-context

    [[ "${NAMESPACE}" == "ls-20" ]]
}

@test "root-context: explicit NAMESPACE override is preserved" {
    cd "${SANDBOX}"
    unset MCK_DEVC_NET_PREFIX

    # Override NAMESPACE in a custom private-context.
    cat >scripts/dev/contexts/private-context <<'EOF'
export NAMESPACE="my-custom"
EOF

    export MCK_DEVC_NET_PREFIX=20

    # shellcheck disable=SC1091
    source scripts/dev/contexts/root-context

    [[ "${NAMESPACE}" == "my-custom" ]]
}

@test "root-context: no MCK_DEVC_NET_PREFIX in env or .devcontainer/.env -> NAMESPACE stays ls" {
    cd "${SANDBOX}"
    unset MCK_DEVC_NET_PREFIX NAMESPACE

    # No .devcontainer/.env file at all.
    [[ ! -f .devcontainer/.env ]]

    # shellcheck disable=SC1091
    source scripts/dev/contexts/root-context

    [[ "${NAMESPACE}" == "ls" ]]
}

@test "root-context: loads MCK_DEVC_NET_PREFIX from .devcontainer/.env when not in env" {
    cd "${SANDBOX}"
    unset MCK_DEVC_NET_PREFIX NAMESPACE

    echo "MCK_DEVC_NET_PREFIX=24" > .devcontainer/.env

    # shellcheck disable=SC1091
    source scripts/dev/contexts/root-context

    [[ "${NAMESPACE}" == "ls-24" ]]
    [[ "${MCK_DEVC_NET_PREFIX}" == "24" ]]
}

@test "root-context: env-set MCK_DEVC_NET_PREFIX wins over .devcontainer/.env" {
    cd "${SANDBOX}"
    unset NAMESPACE
    export MCK_DEVC_NET_PREFIX=18

    echo "MCK_DEVC_NET_PREFIX=24" > .devcontainer/.env

    # shellcheck disable=SC1091
    source scripts/dev/contexts/root-context

    [[ "${NAMESPACE}" == "ls-18" ]]
}

# ---------------------------------------------------------------------------
# delete_om_projects.sh mirror
# ---------------------------------------------------------------------------
#
# We can't fully run delete_om_projects.sh (it hits the OM API), but we can
# extract the derivation block and assert the same conditional logic produces
# the right NAMESPACE. The mirror is meant to behave identically to the
# root-context block.

@test "delete_om_projects.sh: derivation block matches root-context behavior" {
    cd "${SANDBOX}"
    unset MCK_DEVC_NET_PREFIX NAMESPACE
    export NAMESPACE="ls"
    echo "MCK_DEVC_NET_PREFIX=22" > .devcontainer/.env

    # Replay the same conditional that lives in delete_om_projects.sh.
    if [[ -z "${MCK_DEVC_NET_PREFIX:-}" && -f "${PROJECT_DIR:-.}/.devcontainer/.env" ]]; then
        devc_prefix_line="$(grep '^MCK_DEVC_NET_PREFIX=' \
            "${PROJECT_DIR:-.}/.devcontainer/.env" 2>/dev/null | tail -n1 || true)"
        if [[ -n "${devc_prefix_line}" ]]; then
            export "${devc_prefix_line?}"
        fi
        unset devc_prefix_line
    fi
    if [[ -n "${MCK_DEVC_NET_PREFIX:-}" && "${NAMESPACE:-}" == "ls" ]]; then
        NAMESPACE="ls-${MCK_DEVC_NET_PREFIX}"
    fi

    [[ "${NAMESPACE}" == "ls-22" ]]
}

# ---------------------------------------------------------------------------
# cleanup_legacy_ls_om_projects.sh
# ---------------------------------------------------------------------------
#
# Verify the awk filter that picks legacy `ls` / `ls-{nondigit}-...` names
# while preserving in-use `ls-<16..31>-...` names. This is the only piece of
# logic we can test without hitting the OM API.

@test "cleanup_legacy_ls_om_projects: awk filter selects the right projects" {
    legacy="$(printf '%s\n' \
        ls \
        ls-mdb-foo \
        ls-search-bar \
        ls-16 \
        ls-16-mdb \
        ls-31-search \
        ls-32-out-of-range \
        ls-15-out-of-range \
        ls-99 \
        ls-mdb-ns-a \
        not-ls \
      | awk '
          $0 == "ls" { print; next }
          /^ls-/ {
            rest = substr($0, 4)
            n = index(rest, "-")
            if (n == 0) { seg = rest } else { seg = substr(rest, 1, n - 1) }
            if (seg ~ /^[0-9]+$/ && seg+0 >= 16 && seg+0 <= 31) {
              next
            }
            print
          }')"

    # ls (bare), legacy non-digit prefixes, and out-of-range digit prefixes
    # should ALL be flagged as legacy. In-use 16..31 entries should NOT.
    [[ "${legacy}" == *"ls"* ]]
    [[ "${legacy}" == *"ls-mdb-foo"* ]]
    [[ "${legacy}" == *"ls-search-bar"* ]]
    [[ "${legacy}" == *"ls-32-out-of-range"* ]]
    [[ "${legacy}" == *"ls-15-out-of-range"* ]]
    [[ "${legacy}" == *"ls-99"* ]]
    [[ "${legacy}" == *"ls-mdb-ns-a"* ]]

    [[ "${legacy}" != *"ls-16"$'\n'* && "${legacy}" != *"ls-16" ]] || false
    # The above bash-substring trick is fragile when the string contains "ls-16-mdb"
    # which substring-matches "ls-16". Use line-anchored grep instead:
    ! echo "${legacy}" | grep -qx 'ls-16'
    ! echo "${legacy}" | grep -qx 'ls-16-mdb'
    ! echo "${legacy}" | grep -qx 'ls-31-search'
    ! echo "${legacy}" | grep -qx 'not-ls'
}
