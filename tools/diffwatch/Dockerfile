FROM alpine:latest

RUN apk add --update --no-cache python3 py3-pip && ln -sf python3 /usr/bin/python
RUN apk add bash tmux kubectl htop less fzf yq lnav jq curl
RUN pip install tmuxp --break-system-packages

# Create directory for lnav formats
RUN mkdir -p /root/.lnav/formats/installed/

COPY bin_linux/diffwatch /usr/local/bin/
COPY lnav/*.json /root/.lnav/formats/installed/
ADD retry_cmd.sh /usr/local/bin/
RUN chmod +x /usr/local/bin/retry_cmd.sh

CMD ["/bin/bash"]
