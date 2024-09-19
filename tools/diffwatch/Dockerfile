FROM nicolaka/netshoot:latest

RUN apk add tmux kubectl htop less fzf yq lnav

RUN pip install tmuxp --break-system-packages

COPY bin_linux/diffwatch /usr/local/bin/
RUN chmod +x /usr/local/bin/retry_cmd.sh

CMD ["/bin/bash"]
