# MCK devcontainer shell init.
#
# Loaded from /home/vscode/.bashrc and /home/vscode/.zshrc on every shell
# start (sourced, not exec'd, so it cannot use shebang/set semantics).
#
# Two responsibilities:
#   1. For interactive shells, cd into /workspace, source the per-context
#      .generated/context.export.env, and activate the project venv. This
#      means every tmux pane and every `dc_attach.sh <cmd>` shell starts
#      with the same env the e2e scripts assume.
#   2. For interactive shells with no $TMUX (i.e. the user just attached
#      to the devcontainer), exec the 'mck' tmuxp session. Set
#      MCK_NO_TMUX=1 to opt out (dc_attach.sh does this when given args).

if [[ $- == *i* ]]; then
    cd /workspace 2>/dev/null || true
    # Source the per-side env (logical + site) and activate venv via the
    # canonical bootstrap. devenv detects /.dockerenv and picks
    # context.devc.env automatically. If files are missing (on-create
    # not finished, or fresh worktree without make switch), devenv
    # prints a loud warning and the rest of shell-init still runs.
    if [[ -f /workspace/scripts/dev/devenv ]]; then
        # shellcheck disable=SC1091
        . /workspace/scripts/dev/devenv || true
    fi
fi

# mck-env: ergonomic re-source after `make switch`. Defined for every
# shell, interactive or not, so scripts and dev shells share the same
# entry point. Fails non-zero if files are missing — propagates to caller.
mck-env() {
    # shellcheck disable=SC1091
    . /workspace/scripts/dev/devenv
}

if [[ -z "${TMUX:-}" && $- == *i* && -z "${MCK_NO_TMUX:-}" ]]; then
    if command -v tmuxp >/dev/null 2>&1 \
            && [[ -f /workspace/.devcontainer/tmuxp/mck.yaml ]]; then
        exec tmuxp load -y /workspace/.devcontainer/tmuxp/mck.yaml
    else
        exec tmux new-session -A -s mck
    fi
fi
