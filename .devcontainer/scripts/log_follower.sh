#!/usr/bin/env bash
#
# Robustly follow a log file inside a tmuxp pane.
#
# Used by the 'mck' tmuxp session to keep an operator-log pane and an
# e2e-log pane permanently visible, regardless of whether the file
# currently exists, was truncated, was replaced (new inode under the
# same path / symlink target swap), or the underlying viewer crashed.
#
# Waits for the file to appear, then `tail -F` (follows by name, survives
# truncation / rotation / replacement); restarts the loop with a banner if
# tail ever exits.
#
# Usage:
#   log_follower.sh <file> [label]
#
# Notes:
#   - We deliberately use `tail -F` rather than `less +F`: less's
#     follow-mode does not survive file replacement (it holds the old
#     fd), and the panes need to keep showing fresh content with no
#     user intervention. Use tmux's scrollback (prefix [) for paging.
#

set +e
file="${1:?usage: log_follower.sh <file> [label]}"
label="${2:-$(basename "${file}")}"

banner() {
    # Clear screen, home cursor, print one status line.
    printf '\033[2J\033[H[%s] %s\n' "${label}" "$1"
}

while true; do
    if [[ ! -e "${file}" ]]; then
        banner "waiting for ${file} ..."
        while [[ ! -e "${file}" ]]; do sleep 1; done
        banner "following ${file}"
    fi
    # -F  = follow by name (handles truncation, rotation, replacement)
    # -n +1 = include any pre-existing content from the start
    tail -F -n +1 "${file}" 2>/dev/null
    banner "tail exited; restarting in 1s"
    sleep 1
done
