FROM  --platform=${BUILDPLATFORM} registry.access.redhat.com/ubi9/ubi-minimal AS tools_downloader

ARG TARGETPLATFORM
ARG mongodb_tools_url
ARG mongodb_tools_version_s390x
ARG mongodb_tools_version_ppc64le
ARG mongodb_tools_version_amd64
ARG mongodb_tools_version_arm64

RUN microdnf -y update --nodocs \
  && microdnf -y install --nodocs tar gzip \
  && microdnf clean all

RUN case ${TARGETPLATFORM} in \
     "linux/amd64")   export MONGODB_TOOLS_VERSION=${mongodb_tools_version_amd64}    ;; \
     "linux/arm64")   export MONGODB_TOOLS_VERSION=${mongodb_tools_version_arm64}    ;; \
     "linux/s390x")   export MONGODB_TOOLS_VERSION=${mongodb_tools_version_s390x}    ;; \
     "linux/ppc64le") export MONGODB_TOOLS_VERSION=${mongodb_tools_version_ppc64le}  ;; \
  esac \
    && mkdir -p /tools \
    && curl -o /tools/mongodb_tools.tgz "${mongodb_tools_url}/${MONGODB_TOOLS_VERSION}"

RUN tar xfz /tools/mongodb_tools.tgz \
    && mv mongodb-database-tools-*/bin/* /tools \
    && chmod +x /tools/* \
    && rm /tools/mongodb_tools.tgz \
    && rm -r mongodb-database-tools-*

FROM --platform=${BUILDPLATFORM} registry.access.redhat.com/ubi9/ubi-minimal AS agent_downloader

ARG TARGETPLATFORM
ARG mongodb_agent_url
ARG mongodb_agent_version_s390x
ARG mongodb_agent_version_ppc64le
ARG mongodb_agent_version_amd64
ARG mongodb_agent_version_arm64

RUN microdnf -y update --nodocs \
  && microdnf -y install --nodocs tar gzip \
  && microdnf clean all

RUN case ${TARGETPLATFORM} in \
     "linux/amd64")   export MONGODB_AGENT_VERSION=${mongodb_agent_version_amd64}    ;; \
     "linux/arm64")   export MONGODB_AGENT_VERSION=${mongodb_agent_version_arm64}    ;; \
     "linux/s390x")   export MONGODB_AGENT_VERSION=${mongodb_agent_version_s390x}    ;; \
     "linux/ppc64le") export MONGODB_AGENT_VERSION=${mongodb_agent_version_ppc64le}  ;; \
  esac \
    && mkdir -p /agent \
    && curl -o /agent/mongodb_agent.tgz "${mongodb_agent_url}/${MONGODB_AGENT_VERSION}"

RUN tar xfz /agent/mongodb_agent.tgz \
    && mv mongodb-mms-automation-agent-*/mongodb-mms-automation-agent /agent/mongodb-agent \
    && chmod +x /agent/mongodb-agent \
    && rm /agent/mongodb_agent.tgz \
    && rm -r mongodb-mms-automation-agent-*

FROM registry.access.redhat.com/ubi9/ubi-micro AS micro

# ubi-micro has no package manager: packages are installed into a copy of its rootfs from a full UBI image.
# Runs on the target platform because RPMs and their scriptlets are architecture specific.
FROM registry.access.redhat.com/ubi9/ubi AS rootfs_builder
COPY --from=micro / /mnt/rootfs
# - nss_wrapper: LD_PRELOADed by agent-launcher.sh for OpenShift random UIDs
# - cyrus-sasl*, krb5-libs, openldap, openssl, xz-libs, libcurl: mongod runtime libraries
#   (https://www.mongodb.com/docs/manual/tutorial/install-mongodb-enterprise-on-red-hat-tarball/), used by the
#   mongod binary of the static architecture which runs in this container
# - net-snmp*: agent and MongoDB 6.0 SNMP support
# - the rest: commands used by the agent launcher scripts
RUN dnf -y --installroot=/mnt/rootfs --setopt=reposdir=/etc/yum.repos.d --releasever=9 \
        --setopt=install_weak_deps=0 --setopt=tsflags=nodocs --disableplugin=subscription-manager \
        upgrade \
    && dnf -y --installroot=/mnt/rootfs --setopt=reposdir=/etc/yum.repos.d --releasever=9 \
        --setopt=install_weak_deps=0 --setopt=tsflags=nodocs --disableplugin=subscription-manager \
        install \
        curl libcurl \
        nss_wrapper \
        cyrus-sasl cyrus-sasl-gssapi cyrus-sasl-plain krb5-libs openldap openssl xz-libs \
        net-snmp net-snmp-agent-libs \
        hostname tar gzip procps-ng jq findutils grep sed gawk \
    && dnf --installroot=/mnt/rootfs clean all \
    # rpm-libs (needed by net-snmp-agent-libs, linked by MongoDB 6.0) requires the rpm package manager itself.
    # Erase it so that no package manager ends up in the image, while keeping the rpm database accurate for scanners.
    && rpm --root=/mnt/rootfs --erase --nodeps rpm \
    && rm -rf /mnt/rootfs/var/cache/dnf /mnt/rootfs/var/log/dnf* /mnt/rootfs/var/log/hawkey.log

FROM registry.access.redhat.com/ubi9/ubi-micro

COPY --from=rootfs_builder /mnt/rootfs/ /

RUN mkdir -p /agent \
    && mkdir -p /var/lib/mongodb-mms-automation \
      && mkdir -p /var/log/mongodb-mms-automation/ \
      && chmod -R +wr /var/log/mongodb-mms-automation/ \
      # ensure that the agent user can write the logs in OpenShift
      && touch /var/log/mongodb-mms-automation/readiness.log \
      && chmod ugo+rw /var/log/mongodb-mms-automation/readiness.log

# Copy scripts to a safe location that won't be overwritten by volume mount
COPY ./docker/mongodb-kubernetes-init-database/content/LICENSE /licenses/LICENSE
COPY ./docker/mongodb-agent/agent-launcher-shim.sh /usr/local/bin/agent-launcher-shim.sh
COPY ./docker/mongodb-agent/setup-agent-files.sh /usr/local/bin/setup-agent-files.sh
COPY ./docker/mongodb-agent/dummy-probe.sh /usr/local/bin/dummy-probe.sh
COPY ./docker/mongodb-agent/dummy-readinessprobe /usr/local/bin/dummy-readinessprobe

RUN mkdir -p /var/lib/automation/config \
    && chmod -R +r /var/lib/automation/config

RUN mkdir -p /tools /agent

COPY --from=tools_downloader "/tools/" /tools/
COPY --from=agent_downloader "/agent/" /agent/

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
