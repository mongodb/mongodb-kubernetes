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
    if [[ -f /workspace/.generated/context.export.env ]]; then
        # context.export.env already uses 'export', so plain source works
        # for both bash and zsh.
        # shellcheck disable=SC1091
        source /workspace/.generated/context.export.env
    fi
    if [[ -f /workspace/venv/bin/activate ]]; then
        # shellcheck disable=SC1091
        source /workspace/venv/bin/activate
    fi
fi

if [[ -z "${TMUX:-}" && $- == *i* && -z "${MCK_NO_TMUX:-}" ]]; then
    if command -v tmuxp >/dev/null 2>&1 \
            && [[ -f /workspace/.devcontainer/tmuxp/mck.yaml ]]; then
        exec tmuxp load -y /workspace/.devcontainer/tmuxp/mck.yaml
    else
        exec tmux new-session -A -s mck
    fi
fi
