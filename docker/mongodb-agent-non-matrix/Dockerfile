FROM scratch AS base

ARG agent_version
ARG agent_distro
ARG tools_version
ARG tools_distro

ADD https://mciuploads.s3.amazonaws.com/mms-automation/mongodb-mms-build-agent/builds/automation-agent/prod/mongodb-mms-automation-agent-${agent_version}.${agent_distro}.tar.gz /data/mongodb-agent.tar.gz
ADD https://downloads.mongodb.org/tools/db/mongodb-database-tools-${tools_distro}-${tools_version}.tgz /data/mongodb-tools.tgz

COPY ./docker/mongodb-kubernetes-init-database/content/LICENSE /data/LICENSE

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
RUN  microdnf install -y libssh libpsl libbrotli \
    && microdnf download curl libcurl \
    && rpm -Uvh --nodeps --replacefiles "*curl*$( uname -i ).rpm" \
    && microdnf remove -y libcurl-minimal curl-minimal

RUN microdnf install -y --disableplugin=subscription-manager --setopt=install_weak_deps=0 nss_wrapper
# Copy-pasted from https://www.mongodb.com/docs/manual/tutorial/install-mongodb-enterprise-on-red-hat-tarball/
RUN microdnf install -y --disableplugin=subscription-manager \
 cyrus-sasl cyrus-sasl-gssapi cyrus-sasl-plain krb5-libs openldap openssl xz-libs
# Dependencies for the Agent
RUN microdnf install -y --disableplugin=subscription-manager  --setopt=install_weak_deps=0 \
        net-snmp \
        net-snmp-agent-libs
RUN microdnf install -y --disableplugin=subscription-manager \
    hostname tar gzip procps jq \
    && microdnf upgrade -y  \
    && rm -rf /var/lib/apt/lists/*

RUN mkdir -p /agent \
    && mkdir -p /var/lib/mongodb-mms-automation \
      && mkdir -p /var/log/mongodb-mms-automation/ \
      && chmod -R +wr /var/log/mongodb-mms-automation/ \
      # ensure that the agent user can write the logs in OpenShift
      && touch /var/log/mongodb-mms-automation/readiness.log \
      && chmod ugo+rw /var/log/mongodb-mms-automation/readiness.log


COPY --from=base /data/mongodb-agent.tar.gz /agent
COPY --from=base /data/mongodb-tools.tgz /agent
COPY --from=base /data/LICENSE /licenses/LICENSE

# Copy scripts to a safe location that won't be overwritten by volume mount
COPY --from=base /opt/scripts/agent-launcher-shim.sh /usr/local/bin/agent-launcher-shim.sh
COPY --from=base /opt/scripts/setup-agent-files.sh /usr/local/bin/setup-agent-files.sh
COPY --from=base /opt/scripts/dummy-probe.sh /usr/local/bin/dummy-probe.sh
COPY --from=base /opt/scripts/dummy-readinessprobe.sh /usr/local/bin/dummy-readinessprobe

RUN tar xfz /agent/mongodb-agent.tar.gz \
    && mv mongodb-mms-automation-agent-*/mongodb-mms-automation-agent /agent/mongodb-agent \
    && chmod +x /agent/mongodb-agent \
    && mkdir -p /var/lib/automation/config \
    && chmod -R +r /var/lib/automation/config \
    && rm /agent/mongodb-agent.tar.gz \
    && rm -r mongodb-mms-automation-agent-*

RUN tar xfz /agent/mongodb-tools.tgz --directory /var/lib/mongodb-mms-automation/ && rm /agent/mongodb-tools.tgz

USER 2000
CMD ["/agent/mongodb-agent", "-cluster=/var/lib/automation/config/automation-config.json"]
