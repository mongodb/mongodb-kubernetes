# MCK devcontainer shell init.
#
# Loaded from /home/vscode/.bashrc and /home/vscode/.zshrc on every shell
# start (sourced, not exec'd, so it cannot use shebang/set semantics).
#
# Two responsibilities:
#   1. ALWAYS source the per-side env (.generated/context.env +
#      .generated/context.devc.env via scripts/dev/devenv) and activate the
#      project venv whenever devenv exists, regardless of shell mode.
#      Bash interactive panes, zsh tmuxp panes, and orchestrator-spawned
#      `bash -lc` shells must all see the same PROJECT_DIR / KUBECONFIG /
#      NAMESPACE / PATH. Do not gate this on `[[ $- == *i* ]]`: some zsh
#      contexts (tmuxp panes) aren't flagged interactive and would miss the
#      project env.
#   2. For interactive shells with no $TMUX (i.e. the user just attached
#      to the devcontainer), exec the 'mck' tmuxp session. Set
#      MCK_NO_TMUX=1 to opt out (wt-ctl attach does this when given args).

# The tooling checkout mount: carries scripts/dev/devenv, wt_ctl and the
# tmuxp layout. /workspace may be a worktree WITHOUT the tooling (portable
# wt-ctl mode); fall back to /workspace for containers created before the
# mount existed.
MCK_TOOLING="/mck-tooling"
if [[ ! -f "${MCK_TOOLING}/scripts/dev/devenv" ]]; then
    MCK_TOOLING="/workspace"
fi

if [[ -f "${MCK_TOOLING}/scripts/dev/devenv" ]]; then
    cd /workspace 2>/dev/null || true
    # devenv detects /.dockerenv and picks context.devc.env automatically.
    # If files are missing (on-create not finished, or fresh worktree
    # without `make switch`), devenv prints a loud warning and we
    # continue.
    # shellcheck disable=SC1090,SC1091
    . "${MCK_TOOLING}/scripts/dev/devenv" || true
fi

# mck-env: ergonomic re-source after `make switch`. Defined for every
# shell, interactive or not, so scripts and dev shells share the same
# entry point. Fails non-zero if files are missing — propagates to caller.
mck-env() {
    local __t="/mck-tooling"
    [[ -f "${__t}/scripts/dev/devenv" ]] || __t="/workspace"
    # shellcheck disable=SC1090,SC1091
    . "${__t}/scripts/dev/devenv"
}

if [[ -z "${TMUX:-}" && $- == *i* && -z "${MCK_NO_TMUX:-}" ]]; then
    if command -v tmuxp >/dev/null 2>&1 \
            && [[ -f "${MCK_TOOLING}/.devcontainer/tmuxp/mck.yaml" ]]; then
        exec tmuxp load -y "${MCK_TOOLING}/.devcontainer/tmuxp/mck.yaml"
    else
        exec tmux new-session -A -s mck
    fi
fi
