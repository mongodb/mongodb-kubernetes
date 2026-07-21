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
#         k9s --kubeconfig /workspace/.generated/current.devc.kubeconfig
#
# The command (joined and quoted) is pre-loaded into ${HISTFILE}, then run;
# on exit (clean or Ctrl-C) an interactive zsh login shell is exec'd in its
# place so the pane stays alive and Up recalls the command.
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

# Catch (and ignore) SIGINT in the wrapper itself so a Ctrl-C in the
# pane kills the wrapped command but doesn't kill *us* — the kernel
# still delivers SIGINT to every process in the foreground process
# group, so the child gets the signal directly; we just refuse to die.
# Without this, bash exits with rc=130 after the child returns and tmux
# closes the pane before we can `exec` the recovery shell below. (k9s
# panes survived without this trap only because k9s installs its own
# SIGINT handler and doesn't exit on Ctrl-C, so bash here stays parked
# in `wait`. Anything that *does* exit on Ctrl-C — tail -F, sleep loops,
# scripts — would otherwise take the pane down with it.)
trap ':' INT
"$@"
rc=$?
trap - INT

echo
echo "[pane_runner] command exited with code ${rc}; dropping into shell."
echo "[pane_runner] press Up / run !! to recall: ${quoted}"
echo

if command -v zsh >/dev/null 2>&1; then
  exec zsh -l
else
  exec bash -l
fi
