FROM python:3.6

ADD https://storage.googleapis.com/kubernetes-release/release/v1.13.3/bin/linux/amd64/kubectl /usr/bin
RUN chmod +x /usr/bin/kubectl
COPY start.sh /
COPY is_older_than.py /

CMD [ "/start.sh" ]
