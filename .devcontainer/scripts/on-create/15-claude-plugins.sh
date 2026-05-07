#!/bin/bash
# Configure the mck-dev Claude plugin from the private 10gen marketplace.
#
# Re-runs on every container creation so a fresh ~/.claude state always lands
# on the SSH-based marketplace. The host-forwarded ssh-agent (SSH_AUTH_SOCK
# from compose.user.yml) is owned by vscode directly, so no fan-out is
# required at this point in the lifecycle.

set -euo pipefail

if [[ -z "${SSH_AUTH_SOCK:-}" ]] || [[ ! -S "${SSH_AUTH_SOCK}" ]]; then
    echo "SSH agent not forwarded; skipping claude marketplace setup."
    echo "Run manually after attaching:"
    echo "  claude plugin marketplace add git@github.com:10gen/core-platforms-ai-tools.git"
    echo "  claude plugin install mck-dev@core-platforms-ai-tools"
    exit 0
fi

# Run as vscode so writes land in the bind-mounted ~/.claude state and ssh
# uses vscode's ~/.ssh/known_hosts. --preserve-env keeps SSH_AUTH_SOCK
# pointing at the forwarded socket; -H sets HOME to /home/vscode.
sudo -u vscode --preserve-env=SSH_AUTH_SOCK -H bash <<'EOS'
    set -euo pipefail
    export PATH="$HOME/.local/bin:$PATH"

    mkdir -p ~/.ssh && chmod 700 ~/.ssh
    touch ~/.ssh/known_hosts && chmod 644 ~/.ssh/known_hosts
    # Pre-seed github.com host keys — the non-interactive equivalent of
    # `ssh -T git@github.com` answering "yes" — so marketplace add doesn't
    # block on confirmation.
    ssh-keyscan -t rsa,ecdsa,ed25519 github.com 2>/dev/null \
        >> ~/.ssh/known_hosts || true
    sort -u ~/.ssh/known_hosts -o ~/.ssh/known_hosts

    # Remove first so we transition cleanly off any HTTPS-based entry
    # the host may have set up; a missing entry is fine.
    claude plugin marketplace remove core-platforms-ai-tools >/dev/null 2>&1 || true
    claude plugin marketplace add git@github.com:10gen/core-platforms-ai-tools.git
    claude plugin install mck-dev@core-platforms-ai-tools
EOS
