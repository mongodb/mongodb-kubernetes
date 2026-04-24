#!/bin/bash
#
# Forward the host's ssh-agent socket into the main devcontainer service so
# post-start.sh can fan it out to the autossh sidecar.
#
# VS Code's devcontainer extension does this automatically; the bare
# 'devcontainer' CLI does not. Without it autossh blocks forever waiting on
# /ssh-agent/socket and every kubectl call through gost-proxy returns
# 'Service Unavailable'.
#
# Source paths:
#   macOS / Docker Desktop  -> /run/host-services/ssh-auth.sock (provided by
#                              Docker Desktop's SSH agent forwarding)
#   Linux                   -> $SSH_AUTH_SOCK from the host shell (must be
#                              set when initializeCommand runs)
#

set -euo pipefail

case "$(uname -s)" in
    Darwin) host_sock="/run/host-services/ssh-auth.sock" ;;
    Linux)  host_sock="${SSH_AUTH_SOCK:-}" ;;
    *)      host_sock="" ;;
esac

if [[ -z "${host_sock}" ]]; then
    echo "ssh-agent.sh: no host ssh-agent socket detected; skipping"
    exit 0
fi

# environment in compose.generated.yml is a list ('- KEY=VALUE'); check by
# searching for the SSH_AUTH_SOCK= prefix and skip if already added.
if yq eval ".services.devcontainer.environment[]? | select(. == \"SSH_AUTH_SOCK=/ssh-auth.sock\")" "${COMPOSE_OVERRIDE_FILE}" | grep -q .; then
    exit 0
fi

yq eval -i ".services.devcontainer.volumes += [\"${host_sock}:/ssh-auth.sock\"]" "${COMPOSE_OVERRIDE_FILE}"
yq eval -i ".services.devcontainer.environment += [\"SSH_AUTH_SOCK=/ssh-auth.sock\"]" "${COMPOSE_OVERRIDE_FILE}"
echo "ssh-agent.sh: mounted ${host_sock} -> /ssh-auth.sock and exported SSH_AUTH_SOCK"
