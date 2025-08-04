FROM scratch AS tools_downloader

ARG mongodb_tools_url

ARG mongodb_tools_version_amd64
ADD "${mongodb_tools_url}/${mongodb_tools_version_amd64}" /data/amd64/mongodb_tools.tgz

ARG mongodb_tools_version_arm64
ADD "${mongodb_tools_url}/${mongodb_tools_version_arm64}" /data/arm64/mongodb_tools.tgz

ARG mongodb_tools_version_s390x
ADD "${mongodb_tools_url}/${mongodb_tools_version_s390x}" /data/s390x/mongodb_tools.tgz

ARG mongodb_tools_version_ppc64le
ADD "${mongodb_tools_url}/${mongodb_tools_version_ppc64le}" /data/ppc64le/mongodb_tools.tgz

FROM scratch AS agent_downloader

ARG mongodb_agent_url

ARG mongodb_agent_version_amd64
ADD "${mongodb_agent_url}/${mongodb_agent_version_amd64}" /data/amd64/mongodb_agent.tgz

ARG mongodb_agent_version_arm64
ADD "${mongodb_agent_url}/${mongodb_agent_version_arm64}" /data/arm64/mongodb_agent.tgz

ARG mongodb_agent_version_s390x
ADD "${mongodb_agent_url}/${mongodb_agent_version_s390x}" /data/s390x/mongodb_agent.tgz

ARG mongodb_agent_version_ppc64le
ADD "${mongodb_agent_url}/${mongodb_agent_version_ppc64le}" /data/ppc64le/mongodb_agent.tgz

FROM registry.access.redhat.com/ubi9/ubi-minimal

ARG TARGETARCH
COPY --from=tools_downloader "/data/${TARGETARCH}/mongodb_tools.tgz" /tools/mongodb_tools.tgz
COPY --from=agent_downloader "/data/${TARGETARCH}/mongodb_agent.tgz" /agent/mongodb_agent.tgz
COPY ./docker/mongodb-kubernetes-init-database/content/LICENSE /data/LICENSE

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

RUN tar xfz /agent/mongodb_agent.tgz \
    && mv mongodb-mms-automation-agent-*/mongodb-mms-automation-agent /agent/mongodb-agent \
    && chmod +x /agent/mongodb-agent \
    && mkdir -p /var/lib/automation/config \
    && chmod -R +r /var/lib/automation/config \
    && rm /agent/mongodb_agent.tgz \
    && rm -r mongodb-mms-automation-agent-*

RUN tar xfz /tools/mongodb_tools.tgz --directory /var/lib/mongodb-mms-automation/ && rm /tools/mongodb_tools.tgz

ARG version

LABEL name="MongoDB Agent" \
      version="${version}" \
      summary="MongoDB Agent" \
      description="MongoDB Agent" \
      vendor="MongoDB" \
      release="1" \
      maintainer="support@mongodb.com"

USER 2000
CMD ["/agent/mongodb-agent", "-cluster=/var/lib/automation/config/automation-config.json"]
