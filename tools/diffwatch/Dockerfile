FROM alpine:latest

# Set SHELL environment variable
ENV SHELL=/bin/bash

# Install bash first (using default /bin/sh), then other packages
RUN apk add --update --no-cache bash python3 py3-pip && ln -sf python3 /usr/bin/python

# Now switch to bash for subsequent RUN commands
SHELL ["/bin/bash", "-c"]

RUN apk add tmux kubectl htop less fzf yq lnav jq curl base64 git
RUN pip install tmuxp --break-system-packages

COPY bin_linux/diffwatch /usr/local/bin/
ADD retry_cmd.sh /usr/local/bin/
RUN chmod +x /usr/local/bin/retry_cmd.sh

RUN curl -L https://iterm2.com/shell_integration/install_shell_integration_and_utilities.sh -o install_shell_integration_and_utilities.sh \
  && chmod +x install_shell_integration_and_utilities.sh && ./install_shell_integration_and_utilities.sh

# Ensure iTerm shell integration is sourced in .bashrc for interactive shells
RUN echo 'test -e "${HOME}/.iterm2_shell_integration.bash" && source "${HOME}/.iterm2_shell_integration.bash"' >> /root/.bashrc

CMD ["/bin/bash", "-l"]
