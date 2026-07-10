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
#      MCK_NO_TMUX=1 to opt out (dc_attach.sh does this when given args).

if [[ -f /workspace/scripts/dev/devenv ]]; then
    cd /workspace 2>/dev/null || true
    # devenv detects /.dockerenv and picks context.devc.env automatically.
    # If files are missing (on-create not finished, or fresh worktree
    # without `make switch`), devenv prints a loud warning and we
    # continue.
    # shellcheck disable=SC1091
    . /workspace/scripts/dev/devenv || true
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
