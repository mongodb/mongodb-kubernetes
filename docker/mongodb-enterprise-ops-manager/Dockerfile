FROM ubuntu:18.04

ARG OM_DOWNLOAD_LINK

ENV MMS_HOME /mongodb-ops-manager
ENV MMS_PROP_FILE ${MMS_HOME}/conf/conf-mms.properties
ENV MMS_CONF_FILE ${MMS_HOME}/conf/mms.conf
ENV MMS_LOG_DIR ${MMS_HOME}/logs

EXPOSE 8080

RUN apt-get -qq update \
    && apt-get -y -qq install \
        apt-utils \
        ca-certificates \
        curl \
        libsasl2-2 \
        net-tools \
        netcat \
        procps \
    && apt-get upgrade -y -qq \
    && apt-get dist-upgrade -y -qq \
    && rm -rf /var/lib/apt/lists/*

RUN curl --fail -L -o ops_manager.tar.gz "${OM_DOWNLOAD_LINK}" \
    && tar -xzf ops_manager.tar.gz \
    && rm ops_manager.tar.gz \
    && mv mongodb-mms-* "${MMS_HOME}"

# permissions
RUN chmod -R 0775 "${MMS_LOG_DIR}" \
    && chmod -R 0775 "${MMS_HOME}/conf" \
    && chmod -R 0775 "${MMS_HOME}/tmp"

RUN mkdir -p /opt/scripts
COPY scripts /opt/scripts/

# TODO we need to enable USER only when we enable security context on K8s level and make sure it works
#USER 2000

ENTRYPOINT [ "/opt/scripts/docker-entry-point.sh" ]
