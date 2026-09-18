#!/bin/bash

set -euo pipefail

# Debian's stock ~/.bash_logout ends with clear_console, guarded only by
# SHLVL=1. The orchestrator (and every `make` target) runs in-container
# commands as `bash -lc`, so the clear_console failure mode leaks into
# command exit codes: with TERM unset/dumb (non-interactive `devcontainer
# exec`) clear_console prints "\033[3J" + "TERM environment variable not
# set." and exits 1, making successful commands look failed (observed as
# `[preflight] ... TERM environment variable not set.` aborting prepare_e2e).
# Gate on an interactive stdin so only real login sessions clear the screen.
logout="${HOME}/.bash_logout"
if [[ -f "${logout}" ]]; then
    cat >"${logout}" <<'EOF'
# ~/.bash_logout: executed by bash(1) when login shell exits.
# MCK devcontainer: only clear for interactive login sessions — non-tty
# `bash -lc` consumers (orchestrator, make) must not fail on clear_console.
if [[ "$SHLVL" = 1 && -t 0 ]]; then
    [ -x /usr/bin/clear_console ] && /usr/bin/clear_console -q
fi
EOF
fi
