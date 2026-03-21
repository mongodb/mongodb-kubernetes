FROM mcr.microsoft.com/devcontainers/base:ubuntu-24.04

RUN apt-get update && apt-get install -y \
    # Install dependencies for python-ldap
    libldap2-dev \
    libsasl2-dev \
    # Socat for ssh-agent socket forwarding
    screen \
    socat

RUN bash -c 'mkdir -p /workspace/.generated /home/vscode/.cache/{go-build,pip,helm}' && \
    chown -R vscode:vscode /workspace/.generated /home/vscode/.cache

RUN su vscode -c 'mkdir /home/vscode/.operator-dev && touch /home/vscode/.operator-dev/evg-host.kubeconfig'
ARG EVERGREEN_CLI_URL
RUN [ -n "$EVERGREEN_CLI_URL" ] && curl -L "$EVERGREEN_CLI_URL" -o /usr/local/bin/evergreen && chmod +x /usr/local/bin/evergreen
