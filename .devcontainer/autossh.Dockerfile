FROM mcr.microsoft.com/devcontainers/base:ubuntu-24.04

RUN apt-get update && apt-get install -y autossh
COPY evg-host-ssh.conf /etc/ssh/ssh_config.d/evg-host.conf
