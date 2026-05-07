FROM mcr.microsoft.com/devcontainers/base:ubuntu-24.04

# Ensure UTF-8 locale so tmux, prompts, and any tool that prints non-ASCII
# (e.g. starship's ❯, helm/kubectl box-drawing) renders correctly.
ENV LANG=C.UTF-8 LC_ALL=C.UTF-8

RUN apt-get update && apt-get install -y \
    # Install dependencies for python-ldap
    libldap2-dev \
    libsasl2-dev \
    # Log navigator for browsing operator/agent logs
    lnav \
    # Socat for ssh-agent socket forwarding
    screen \
    socat \
    # Terminal multiplexer for the default workflow
    tmux

# Install Claude Code for the vscode user.
# State (auth, plugins, marketplaces, memory) lives under ~/.claude which is
# bind-mounted from the host in compose.yml, so the binary is the only thing
# the image needs to provide.
RUN su -l vscode -c 'curl -fsSL https://claude.ai/install.sh | bash' \
    || echo "Claude Code install failed; install manually with: curl -fsSL https://claude.ai/install.sh | bash"

# Make zsh the default shell for vscode (matches host workflow for users who
# prefer zsh; bash users can still `bash -l` explicitly).
RUN chsh -s /usr/bin/zsh vscode

# Auto-attach interactive shells to a single tmux session named "mck".
# Guarded so it only triggers in interactive shells and never nests.
# Installed in both .bashrc and .zshrc so it fires regardless of login shell.
RUN echo '[[ -z "${TMUX:-}" && $- == *i* ]] && exec tmux new-session -A -s mck' \
    | tee -a /home/vscode/.bashrc /home/vscode/.zshrc > /dev/null

RUN bash -c 'mkdir -p /workspace/.generated /home/vscode/.cache/{go-build,pip,helm}' && \
    chown -R vscode:vscode /workspace/.generated /home/vscode/.cache
