ARG imagebase
FROM ${imagebase} as base

FROM registry.access.redhat.com/ubi9/ubi-minimal as builder

RUN microdnf install -y tar gzip && microdnf clean all

COPY --from=base /data/mongodb-agent.tar.gz /data/mongodb-tools.tgz /tmp/
RUN tar xfz /tmp/mongodb-agent.tar.gz -C /opt && \
    tar xfz /tmp/mongodb-tools.tgz -C /opt && \
    rm /tmp/*.tgz /tmp/*.tar.gz

RUN chmod +x /opt/mongodb-mms-automation-agent-*/mongodb-mms-automation-agent

FROM registry.access.redhat.com/ubi9/ubi-minimal

ARG version

LABEL name="MongoDB Agent" \
      version="${version}" \
      summary="MongoDB Agent" \
      description="MongoDB Agent" \
      vendor="MongoDB" \
      release="1" \
      maintainer="support@mongodb.com"

# Replace libcurl-minimal and curl-minimal with the full versions
# https://bugzilla.redhat.com/show_bug.cgi?id=1994521
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
COPY --from=builder /opt/mongodb-mms-automation-agent-*/mongodb-mms-automation-agent /agent/mongodb-agent
COPY --from=builder /opt/mongodb-database-tools-*/bin/ /usr/local/bin/

USER 2000
CMD ["/agent/mongodb-agent", "-cluster=/var/lib/automation/config/automation-config.json"]
