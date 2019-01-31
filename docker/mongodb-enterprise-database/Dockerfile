# This is the Docker file for the Ops Manager (Enterprise) Database
#
FROM debian:9.5-slim

ENV MMS_HOME /mongodb-automation
ENV MMS_LOG_DIR /var/log/mongodb-mms-automation

# The following list of dependencies was obtained from:
# https://docs.mongodb.com/manual/tutorial/install-mongodb-enterprise-on-debian/#prerequisites
# Also, the Automation Agent needs lsb-release on the CLI to determine the current distro
RUN apt-get -qq update \
      && apt-get -y -qq install \
      ca-certificates \
      curl \
      \
      libcurl3 \
      libgssapi-krb5-2 \
      libkrb5-dbg \
      libldap-2.4-2 \
      libpcap0.8 \
      libsasl2-2 \
      snmp \
      openssl \
      \
      libsasl2-2 \
      lsb-release \
      procps \
      \
      jq \
    && apt-get upgrade -y -qq \
    && apt-get dist-upgrade -y -qq \
    && rm -rf /var/lib/apt/lists/*

# Deploy the contents
COPY content/ ${MMS_HOME}/files

# Set the required perms
RUN    mkdir -p ${MMS_LOG_DIR} \
    && chmod 0775 ${MMS_LOG_DIR} \
    && mkdir -p /var/lib/mongodb-mms-automation \
    && chmod 0775 /var/lib/mongodb-mms-automation \
    && mkdir -p /data \
    && chmod 0775 /data \
    && mkdir -p /journal \
    && chmod 0775 /journal \
    && chmod -R 0775 ${MMS_HOME} \
    && chmod 0664 /etc/passwd

CMD /mongodb-automation/files/agent-launcher.sh
