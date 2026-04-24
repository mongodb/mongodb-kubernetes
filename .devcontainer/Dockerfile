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
    # pip is used a few lines below to install tmuxp into the system
    # python; keeps the layer count small.
    python3-pip \
    # Socat for ssh-agent socket forwarding
    screen \
    socat \
    # Terminal multiplexer for the default workflow
    tmux \
 && pip3 install --break-system-packages tmuxp

# Install Claude Code for the vscode user.
# State (auth, plugins, marketplaces, memory) lives under ~/.claude which is
# bind-mounted from the host in compose.yml, so the binary is the only thing
# the image needs to provide.
RUN su -l vscode -c 'curl -fsSL https://claude.ai/install.sh | bash' \
    || echo "Claude Code install failed; install manually with: curl -fsSL https://claude.ai/install.sh | bash"

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
        '# MCK: also load shell-init.sh for non-interactive login shells' \
        '# (e.g. `bash -lc CMD` from `wt-ctl attach -- bash -lc ...`). The' \
        '# .bashrc / .zshrc sourcing only fires for INTERACTIVE shells.' \
        '# Without this hook, `devcontainer exec ... bash -lc` runs with' \
        '# KUBECONFIG / PROJECT_DIR unset, and kubectl falls back to' \
        '# localhost:8080. Idempotent: shell-init only re-sources devenv.' \
        '# Only run for bash — shell-init.sh uses bash-specific syntax' \
        '# ([[ ]], $-) that breaks under sh/dash. zsh login shells source' \
        '# this via /etc/zsh/zprofile + emulate sh, but `[ -n "$BASH" ]"`' \
        '# excludes them — they get shell-init via .zshrc separately.' \
        '[ -n "${BASH:-}" ] && [ -f /etc/mck/shell-init.sh ] && . /etc/mck/shell-init.sh' \
        > /etc/profile.d/01-mck-shell-init.sh \
 && chmod 0644 /etc/profile.d/01-mck-shell-init.sh

# Add /workspace/bin (per-worktree project binaries: kubectl, helm, etc.
# pinned via Makefile install targets) to PATH for every login shell.
# This replaces the PATH prepend that lived in scripts/dev/set_env_context.sh
# in the old design. Keep it container-only — host devs add the
# equivalent to their ~/.zshrc themselves. See docs/dev/context-split/.
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
