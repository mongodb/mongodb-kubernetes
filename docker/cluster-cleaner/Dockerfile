FROM python:3-slim-buster

ADD https://dl.k8s.io/release/v1.34.0/bin/linux/amd64/kubectl /usr/bin
RUN chmod +x /usr/bin/kubectl

COPY scripts/* /
