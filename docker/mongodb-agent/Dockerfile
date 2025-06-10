ARG imagebase
FROM ${imagebase} AS base

FROM registry.access.redhat.com/ubi9/ubi-minimal AS builder

RUN microdnf install -y tar gzip && microdnf clean all

COPY --from=base /data/mongodb_tools_ubi.tgz /data/mongodb_agent_ubi.tgz /tmp/

RUN tar xfz /tmp/mongodb_tools_ubi.tgz -C /opt && \
    tar xfz /tmp/mongodb_agent_ubi.tgz -C /opt && \
    rm /tmp/*.tgz

RUN chmod +x /opt/mongodb-mms-automation-agent-*/mongodb-mms-automation-agent
RUN chmod +x /opt/mongodb-database-tools-*/bin/*

COPY --from=base /data/probe.sh \
                 /data/readinessprobe \
                 /data/version-upgrade-hook \
                 /data/agent-launcher-lib.sh \
                 /data/agent-launcher.sh \
                 /opt/scripts/

RUN chmod +x  /opt/scripts/*

FROM registry.access.redhat.com/ubi9/ubi-minimal

ARG version

LABEL name="MongoDB Agent" \
      version="${version}" \
      summary="MongoDB Agent" \
      description="MongoDB Agent" \
      vendor="MongoDB" \
      release="1" \
      maintainer="support@mongodb.com"

RUN    microdnf install -y libssh libpsl libbrotli \
    && microdnf download curl libcurl \
    && rpm -Uvh --nodeps --replacefiles "*curl*$( uname -i ).rpm" \
    && microdnf remove -y libcurl-minimal curl-minimal \
    && microdnf install -y --disableplugin=subscription-manager --setopt=install_weak_deps=0 nss_wrapper \
    && microdnf install -y --disableplugin=subscription-manager \
    cyrus-sasl cyrus-sasl-gssapi cyrus-sasl-plain krb5-libs openldap openssl xz-libs \
    && microdnf install -y --disableplugin=subscription-manager  --setopt=install_weak_deps=0 \
           net-snmp \
           net-snmp-agent-libs \
    && microdnf install -y --disableplugin=subscription-manager \
       hostname tar gzip procps jq \
    && microdnf upgrade -y  \
    && microdnf clean all \
    && mkdir -p /agent \
             /var/lib/mongodb-mms-automation \
             /var/log/mongodb-mms-automation \
             /var/lib/automation/config \
    && chmod -R +wr /var/log/mongodb-mms-automation/ \
    && touch /var/log/mongodb-mms-automation/readiness.log \
    && chmod ugo+rw /var/log/mongodb-mms-automation/readiness.log \
    && chmod -R +r /var/lib/automation/config

COPY --from=base /data/LICENSE /licenses/LICENSE

COPY --from=builder /opt/scripts/* /opt/scripts/
COPY --from=builder /opt/mongodb-database-tools-*/bin /tools
COPY --from=builder /opt/mongodb-mms-automation-agent-*/mongodb-mms-automation-agent /agent/mongodb-agent

RUN mkdir -p /var/lib/automation/config \
    && chmod -R +r /var/lib/automation/config

USER 2000

HEALTHCHECK --timeout=30s CMD ls /opt/scripts/readinessprobe || exit 1
