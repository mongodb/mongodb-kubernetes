#!/usr/bin/env bash
#
# Attach to the current worktree's devcontainer.
#
#   dc_attach.sh                  -> enter the 'mck' tmuxp session
#                                    (k9s + operator log + e2e log + zsh)
#   dc_attach.sh bash             -> bare bash inside the container
#   dc_attach.sh zsh              -> bare zsh
#   dc_attach.sh make foo         -> run an arbitrary command
#
# In "with arguments" mode we set MCK_NO_TMUX=1 so the shell-init does
# not auto-exec tmuxp under the user's command. Without that, e.g.
# `dc_attach.sh zsh` would silently switch to the tmuxp session and
# defeat the whole point of asking for a bare shell.
#
# The workspace folder is the worktree this script lives in (so each
# worktree's `dc_attach.sh` always targets that worktree's compose
# stack — no risk of attaching to a different worktree's container).
#
# DEPRECATED: prefer `wt-ctl attach`. This shim execs into wt-ctl so
# existing skills + scripts keep working through the Phase-1/2/3 transition.
#
set -Eeou pipefail
test "${MDB_BASH_DEBUG:-0}" -eq 1 && set -x

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
echo "[deprecated] dc_attach.sh — use 'wt-ctl attach' instead" >&2
exec "${script_dir}/wt-ctl" attach "$@"
