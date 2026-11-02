FROM scratch AS base

COPY ./docker/mongodb-kubernetes-database/LICENSE /data/licenses/mongodb-kubernetes-database

FROM registry.access.redhat.com/ubi9/ubi-micro AS micro

# ubi-micro has no package manager: packages are installed into a copy of its rootfs from a full UBI image.
# Runs on the target platform because RPMs and their scriptlets are architecture specific.
FROM registry.access.redhat.com/ubi9/ubi AS rootfs_builder
COPY --from=micro / /mnt/rootfs
# Only libraries are installed for the MongoDB dependencies, not the daemons/CLIs from the same source packages
# (net-snmp, cyrus-sasl/saslauthd, openssl), which would pull in systemd, perl and more without being used.
# - nss_wrapper-libs (libnss_wrapper.so for OpenShift random UIDs), hostname, procps-ng, curl, jq, tar, gzip,
#   findutils, grep, sed, gawk: agent launcher scripts
# - util-linux-core: mount, referenced by the agent
# - the rest: MongoDB runtime libraries
#   (https://www.mongodb.com/docs/manual/tutorial/install-mongodb-enterprise-on-red-hat-tarball/ "RHEL 9" tab)
RUN dnf -y --installroot=/mnt/rootfs --setopt=reposdir=/etc/yum.repos.d --releasever=9 \
        --setopt=install_weak_deps=0 --setopt=tsflags=nodocs --disableplugin=subscription-manager \
        upgrade \
    && dnf -y --installroot=/mnt/rootfs --setopt=reposdir=/etc/yum.repos.d --releasever=9 \
        --setopt=install_weak_deps=0 --setopt=tsflags=nodocs --disableplugin=subscription-manager \
        install \
        nss_wrapper-libs hostname procps-ng curl jq tar gzip findutils grep sed gawk util-linux-core \
        cyrus-sasl-lib cyrus-sasl-gssapi cyrus-sasl-plain krb5-libs libcurl lm_sensors-libs \
        net-snmp-libs net-snmp-agent-libs openldap openssl-libs xz-libs \
    && dnf --installroot=/mnt/rootfs clean all \
    # rpm-libs (needed by net-snmp-agent-libs, linked by MongoDB 6.0) requires the rpm package manager itself.
    # Erase it so that no package manager ends up in the image, while keeping the rpm database accurate for scanners.
    && rpm --root=/mnt/rootfs --erase --nodeps rpm \
    && rm -rf /mnt/rootfs/var/cache/dnf /mnt/rootfs/var/log/dnf* /mnt/rootfs/var/log/hawkey.log

FROM registry.access.redhat.com/ubi9/ubi-micro

ENV MMS_HOME=/mongodb-automation
ENV MMS_LOG_DIR=/var/log/mongodb-mms-automation

COPY --from=rootfs_builder /mnt/rootfs/ /

# Set the required perms
RUN mkdir -p "${MMS_LOG_DIR}" \
        && chmod 0775 "${MMS_LOG_DIR}" \
        && mkdir -p /var/lib/mongodb-mms-automation \
        && chmod 0775 /var/lib/mongodb-mms-automation \
        && mkdir -p /data \
        && chmod 0775 /data \
        && mkdir -p /journal \
        && chmod 0775 /journal \
        && mkdir -p "${MMS_HOME}" \
        && chmod -R 0775 "${MMS_HOME}"

ARG version

LABEL name="MongoDB Kubernetes Database" \
      version="${version}" \
      summary="MongoDB Kubernetes Database Image" \
      description="MongoDB Kubernetes Database Image" \
      vendor="MongoDB" \
      release="1" \
      maintainer="support@mongodb.com"

# USER needs to be set for this image to pass RedHat verification. Some customers have these requirements as well
# It does not matter what number it is, as long as it is set to something.
# However, OpenShift will run the container as a random user,
# and the number in this configuration is not relevant.
USER 2000

# The docker image doesn't have any scripts so by default does nothing
# The script will be copied in runtime from init containers and the operator is expected
# to override the COMMAND
ENTRYPOINT ["sleep infinity"]

COPY --from=base /data/licenses/mongodb-kubernetes-database /licenses/mongodb-kubernetes-database
