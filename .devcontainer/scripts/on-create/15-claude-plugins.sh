#!/bin/bash
# Configure the mck-dev Claude plugin from the private 10gen marketplace.
#
# DISABLED (for now): running `claude plugin marketplace add/install` here wrote
# a /home/vscode/... installLocation into the host-shared ~/.claude/known_marketplaces.json
# (bind-mounted in compose.yml), which breaks the marketplace on the macOS host
# (cache-miss) every time a container is created. The ~/.claude bind mount
# (compose.yml) and the Claude Code install (Dockerfile) are disabled alongside
# this. Re-enable all three together once the host/container installLocation
# pollution is solved.
#
# To set up the marketplace manually inside the container after attaching:
#   claude plugin marketplace add git@github.com:10gen/core-platforms-ai-tools.git
#   claude plugin install mck-dev@core-platforms-ai-tools

set -euo pipefail

echo "claude marketplace setup disabled (see comment in $0); skipping."
exit 0

# --- Original logic, kept commented for easy re-enable -----------------------
# if [[ -z "${SSH_AUTH_SOCK:-}" ]] || [[ ! -S "${SSH_AUTH_SOCK}" ]]; then
#     echo "SSH agent not forwarded; skipping claude marketplace setup."
#     echo "Run manually after attaching:"
#     echo "  claude plugin marketplace add git@github.com:10gen/core-platforms-ai-tools.git"
#     echo "  claude plugin install mck-dev@core-platforms-ai-tools"
#     exit 0
# fi
#
# # Run as vscode so writes land in the bind-mounted ~/.claude state and ssh
# # uses vscode's ~/.ssh/known_hosts. The bare devcontainer CLI runs
# # onCreateCommand as vscode already; only fall back to sudo when invoked
# # as root (e.g. by tooling that runs lifecycle commands elevated).
# run_as_vscode() {
#     if [[ "$(id -un)" == "vscode" ]]; then
#         bash -c "$1"
#     else
#         sudo -u vscode --preserve-env=SSH_AUTH_SOCK -H bash -c "$1"
#     fi
# }
#
# run_as_vscode '
#     set -euo pipefail
#     export PATH="$HOME/.local/bin:$PATH"
#
#     mkdir -p ~/.ssh && chmod 700 ~/.ssh
#     touch ~/.ssh/known_hosts && chmod 644 ~/.ssh/known_hosts
#     # Pre-seed github.com host keys — the non-interactive equivalent of
#     # `ssh -T git@github.com` answering "yes" — so marketplace add does not
#     # block on confirmation.
#     ssh-keyscan -t rsa,ecdsa,ed25519 github.com 2>/dev/null \
#         >> ~/.ssh/known_hosts || true
#     sort -u ~/.ssh/known_hosts -o ~/.ssh/known_hosts
#
#     # Remove first so we transition cleanly off any HTTPS-based entry
#     # the host may have set up; a missing entry is fine.
#     claude plugin marketplace remove core-platforms-ai-tools >/dev/null 2>&1 || true
#     claude plugin marketplace add git@github.com:10gen/core-platforms-ai-tools.git
#     claude plugin install mck-dev@core-platforms-ai-tools
# '
