FROM mcr.microsoft.com/devcontainers/base:ubuntu-24.04

# Ensure UTF-8 locale so tmux, prompts, and any tool that prints non-ASCII
# (e.g. starship's ❯, helm/kubectl box-drawing) renders correctly.
ENV LANG=C.UTF-8 LC_ALL=C.UTF-8
ENV DEBIAN_FRONTEND=noninteractive

# Quiet apt/dpkg build noise (Get:/Fetched lines, download progress bars, the
# dpkg pty progress redraw). Written before any apt call and inherited by the
# devcontainer features layered on top of this image, so their apt output is
# dampened too. Errors still go to stderr; nothing is hidden on failure.
RUN printf '%s\n' \
        'APT::Get::quiet "2";' \
        'quiet "1";' \
        'Dpkg::Use-Pty "0";' \
        > /etc/apt/apt.conf.d/99mck-quiet

RUN apt-get update && apt-get install -y \
    # Install dependencies for python-ldap
    libldap2-dev \
    libsasl2-dev \
    # Log navigator for browsing operator/agent logs
    lnav \
    # pip is used a few lines below to install tmuxp into the system
    # python; keeps the layer count small.
    python3-pip \
    # Socat for ssh-agent socket forwarding
    screen \
    socat \
    # Terminal multiplexer for the default workflow
    tmux \
 && pip3 install --break-system-packages --quiet tmuxp

# Claude Code is not installed in the container. Install manually after
# attaching: curl -fsSL https://claude.ai/install.sh | bash

# Make zsh the default shell for vscode (matches host workflow for users who
# prefer zsh; bash users can still `bash -l` explicitly).
RUN chsh -s /usr/bin/zsh vscode

# Workspace context loader + tmuxp auto-attach, sourced from .bashrc /
# .zshrc on every shell start. The actual logic lives in shell-init.sh
# (multi-line, version-controlled, easier to edit) — we just bake a
# one-liner sourcing it into both rc files.
COPY shell-init.sh /etc/mck/shell-init.sh
RUN chmod 0644 /etc/mck/shell-init.sh \
 && echo '[[ -f /etc/mck/shell-init.sh ]] && source /etc/mck/shell-init.sh' \
    | tee -a /home/vscode/.bashrc /home/vscode/.zshrc > /dev/null \
 && printf '%s\n' \
        '[ -n "${BASH:-}" ] && [ -f /etc/mck/shell-init.sh ] && . /etc/mck/shell-init.sh' \
        > /etc/profile.d/01-mck-shell-init.sh \
 && chmod 0644 /etc/profile.d/01-mck-shell-init.sh
# 01-mck-shell-init.sh loads shell-init.sh for non-interactive login shells
# (e.g. `bash -lc CMD` from `wt-ctl attach -- bash -lc ...`); the .bashrc /
# .zshrc sourcing above only fires for INTERACTIVE shells. Without this hook,
# `devcontainer exec ... bash -lc` runs with KUBECONFIG / PROJECT_DIR unset and
# kubectl falls back to localhost:8080. Idempotent: shell-init only re-sources
# devenv. Bash-only — shell-init.sh uses bash syntax ([[ ]], $-) that breaks
# under sh/dash; zsh login shells get shell-init via .zshrc separately.

# Add /workspace/bin (per-worktree project binaries: kubectl, helm, etc.
# pinned via Makefile install targets) to PATH for every login shell.
# Container-only — host devs add the equivalent to their ~/.zshrc themselves.
#
# Debian/Ubuntu's stock /etc/zsh/zprofile is comments only — it does NOT
# source /etc/profile, so /etc/profile.d/* never fires for zsh login
# shells. Append an explicit sourcing line so zsh login shells get the
# same PATH as bash login shells.
RUN printf 'export PATH="/workspace/bin:${PATH}"\n' \
        > /etc/profile.d/mck-bin.sh \
 && chmod 0644 /etc/profile.d/mck-bin.sh \
 && printf '\n# MCK: source /etc/profile.d/* for zsh login shells (Debian default zprofile is comment-only).\nemulate sh -c '\''source /etc/profile'\''\n' \
        >> /etc/zsh/zprofile

RUN bash -c 'mkdir -p /workspace/.generated /home/vscode/.cache/{go-build,pip,helm}' && \
    chown -R vscode:vscode /workspace/.generated /home/vscode/.cache
