FROM ubuntu:18.04

ARG OM_DOWNLOAD_LINK

ENV mms_prop_file /opt/mongodb/mms/conf/conf-mms.properties
ENV mms_conf_file /opt/mongodb/mms/conf/mms.conf

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


WORKDIR /opt

ADD ${OM_DOWNLOAD_LINK} ops_manager_deb

RUN dpkg -i ops_manager_deb \
    && rm ops_manager_deb \
    && chown -R mongodb-mms:root /opt/mongodb/mms \
    && chmod -R ug+w /opt/mongodb/mms

RUN chown mongodb-mms:root /etc/mongodb-mms \
    && chmod 0770 /etc/mongodb-mms

RUN mkdir -p /opt/scripts
COPY scripts /opt/scripts/

USER 2000

ENTRYPOINT [ "/opt/scripts/docker-entry-point.sh" ]
