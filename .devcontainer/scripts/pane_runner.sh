#!/usr/bin/env bash
#
# pane_runner.sh — wrap a tmux pane command so Ctrl-C drops the user into
# an interactive shell with the just-killed command sitting in history,
# instead of closing the pane.
#
# Usage:
#   pane_runner.sh <command...>
#
# Example (in tmuxp):
#   shell_command:
#     - exec /workspace/.devcontainer/scripts/pane_runner.sh \
#         k9s --kubeconfig /workspace/.generated/evg-host.devc.kubeconfig
#
# Behaviour:
#   1. The command (joined and quoted) is pre-loaded into ${HISTFILE} so
#      pressing Up / running !! in the resulting shell re-runs it.
#   2. The command runs.
#   3. On exit (clean or Ctrl-C), an interactive zsh login shell is
#      `exec`d in its place. The pane stays alive. The user can:
#         - press Up to recall the original command
#         - edit it
#         - just `exit` to close the pane (same as a normal shell)
#
# We use zsh because the rest of the devcontainer's shell-init lives in
# zsh (.zshrc invokes mck-env which sources context.env + context.devc.env
# via scripts/dev/devenv, activates the venv, etc.).
# Falls back to bash if zsh is missing.

set -u

if [[ $# -lt 1 ]]; then
  echo "usage: $0 <command...>" >&2
  exit 64
fi

# Quote each arg so the recall in shell history reproduces the original
# invocation faithfully (whitespace, special chars).
quoted=""
for a in "$@"; do
  quoted+=" $(printf '%q' "${a}")"
done
quoted="${quoted# }"

# Push into zsh and bash histories — whichever shell ends up running
# the recall in, the entry is there. Use the user's actual home (HOME),
# not /root, because the panes run as the devcontainer user.
zsh_hist="${HOME}/.zsh_history"
bash_hist="${HOME}/.bash_history"
mkdir -p "$(dirname "${zsh_hist}")" 2>/dev/null || true
# zsh extended history format: ': <ts>:0;<cmd>'
printf ': %d:0;%s\n' "$(date +%s)" "${quoted}" >> "${zsh_hist}" 2>/dev/null || true
printf '%s\n' "${quoted}" >> "${bash_hist}" 2>/dev/null || true

# Trap nothing — let SIGINT pass through to the child. When the child
# exits for whatever reason, fall through to the exec below.
"$@"
rc=$?

echo
echo "[pane_runner] command exited with code ${rc}; dropping into shell."
echo "[pane_runner] press Up / run !! to recall: ${quoted}"
echo

if command -v zsh >/dev/null 2>&1; then
  exec zsh -l
else
  exec bash -l
fi
