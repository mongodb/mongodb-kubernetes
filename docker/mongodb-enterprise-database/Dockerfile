# This is the Docker file for Ops Manager Database
#
FROM debian:jessie-slim

# The Automation Agent needs lsb-release on the CLI to determine the current distro
RUN apt-get -qq update && \
  apt-get -y -qq install libsasl2-2 curl supervisor ca-certificates lsb-release snmp && \
  apt-get upgrade -y -qq && \
  apt-get dist-upgrade -y -qq && \
  rm -rf /var/lib/apt/lists/*

COPY content/ /mongodb-automation/files/

RUN \
    mkdir -p /var/log/mongodb-mms-automation && \
    mkdir -p /var/lib/mongodb-mms-automation && \
    mkdir -p /data && \
    mkdir -p /journal && \
    chmod g+w /data && \
    chmod g+w /journal && \
    chmod -R g+w /mongodb-automation && \
    chmod g+w /var/lib/mongodb-mms-automation && \
    chmod g+w /var/log/mongodb-mms-automation && \
    chmod g+w /etc/passwd

CMD supervisord -c /mongodb-automation/files/supervisor.conf
