# Build compilable stuff

FROM public.ecr.aws/docker/library/golang:1.26 AS readiness_builder
COPY . /go/src/github.com/mongodb/mongodb-kubernetes
WORKDIR /go/src/github.com/mongodb/mongodb-kubernetes

RUN CGO_ENABLED=0 go build -a -buildvcs=false -o /data/scripts/mmsconfiguration ./docker/mongodb-kubernetes-init-ops-manager/mmsconfiguration/edit_mms_configuration.go
RUN CGO_ENABLED=0 go build -a -buildvcs=false -o /data/scripts/backup-daemon-readiness-probe ./docker/mongodb-kubernetes-init-ops-manager/backupdaemon_readinessprobe/backupdaemon_readiness.go

# Move binaries and scripts
FROM scratch AS base

COPY --from=readiness_builder /data/scripts/mmsconfiguration /data/scripts/mmsconfiguration
COPY --from=readiness_builder /data/scripts/backup-daemon-readiness-probe /data/scripts/backup-daemon-readiness-probe

# After v2.0, when non-Static Agent images will be removed, please ensure to copy those files
# into ./docker/mongodb-enterprise-ops-manager directory. Leaving it this way will make the maintenance easier.
COPY ./docker/mongodb-kubernetes-init-ops-manager/scripts/docker-entry-point.sh /data/scripts
COPY ./docker/mongodb-kubernetes-init-ops-manager/scripts/backup-daemon-liveness-probe.sh /data/scripts
COPY ./docker/mongodb-kubernetes-init-ops-manager/LICENSE /data/licenses/mongodb-enterprise-ops-manager

FROM --platform=${BUILDPLATFORM} registry.access.redhat.com/ubi9/ubi-minimal AS om_downloader

ARG om_download_url

ENV MMS_HOME=/mongodb-ops-manager
ENV MMS_PROP_FILE=${MMS_HOME}/conf/conf-mms.properties
ENV MMS_CONF_FILE=${MMS_HOME}/conf/mms.conf
ENV MMS_LOG_DIR=${MMS_HOME}/logs
ENV MMS_TMP_DIR=${MMS_HOME}/tmp

RUN microdnf -y install --nodocs tar gzip \
  && microdnf clean all

RUN curl --fail -L -o ops_manager.tar.gz ${om_download_url} \
  && tar -xzf ops_manager.tar.gz \
  && rm ops_manager.tar.gz \
  && mv mongodb-mms* "${MMS_HOME}"

# permissions
RUN chmod -R 0777 "${MMS_LOG_DIR}" \
  && chmod -R 0777 "${MMS_TMP_DIR}" \
  && chmod -R 0775 "${MMS_HOME}/conf" \
  && chmod -R 0775 "${MMS_HOME}/jdk" \
  && mkdir "${MMS_HOME}/mongodb-releases/" \
  && chmod -R 0775 "${MMS_HOME}/mongodb-releases" \
  && chmod -R 0777 "${MMS_CONF_FILE}" \
  && chmod -R 0777 "${MMS_PROP_FILE}"

# The "${MMS_HOME}/conf" will be populated by the docker-entry-point.sh.
# For now we need to move into the templates directory.
RUN cp -r "${MMS_HOME}/conf" "${MMS_HOME}/conf-template"

FROM registry.access.redhat.com/ubi9/ubi-micro AS micro

# ubi-micro has no package manager: packages are installed into a copy of its rootfs from a full UBI image.
# Runs on the target platform because RPMs and their scriptlets are architecture specific.
FROM registry.access.redhat.com/ubi9/ubi AS rootfs_builder
COPY --from=micro / /mnt/rootfs
# OpsManager docker image needs to have the MongoDB dependencies because the
# backup daemon is running its database locally. Only libraries are installed for them, not the daemons/CLIs from the
# same source packages (net-snmp, cyrus-sasl/saslauthd), which would pull in systemd, perl and more without being used.
# - procps-ng, net-tools, ncurses, util-linux, openssl, findutils, grep, sed, gawk: Ops Manager bin/ scripts
RUN dnf -y --installroot=/mnt/rootfs --setopt=reposdir=/etc/yum.repos.d --releasever=9 \
        --setopt=install_weak_deps=0 --setopt=tsflags=nodocs --disableplugin=subscription-manager \
        upgrade \
    && dnf -y --installroot=/mnt/rootfs --setopt=reposdir=/etc/yum.repos.d --releasever=9 \
        --setopt=install_weak_deps=0 --setopt=tsflags=nodocs --disableplugin=subscription-manager \
        install \
        libcurl \
        cyrus-sasl-lib cyrus-sasl-gssapi cyrus-sasl-plain krb5-libs lm_sensors-libs \
        net-snmp-libs net-snmp-agent-libs openldap openssl rpm-libs net-tools procps-ng ncurses \
        util-linux findutils grep sed gawk \
    && dnf --installroot=/mnt/rootfs clean all \
    # rpm-libs (needed by net-snmp-agent-libs, linked by MongoDB 6.0) requires the rpm package manager itself.
    # Erase it so that no package manager ends up in the image, while keeping the rpm database accurate for scanners.
    && rpm --root=/mnt/rootfs --erase --nodeps rpm \
    && rm -rf /mnt/rootfs/var/cache/dnf /mnt/rootfs/var/log/dnf* /mnt/rootfs/var/log/hawkey.log

FROM registry.access.redhat.com/ubi9/ubi-micro

ARG version

LABEL name="MongoDB Enterprise Ops Manager" \
  maintainer="support@mongodb.com" \
  vendor="MongoDB" \
  version=${version} \
  release="1" \
  summary="MongoDB Enterprise Ops Manager Image" \
  description="MongoDB Enterprise Ops Manager"

ENV MMS_HOME=/mongodb-ops-manager
ENV MMS_PROP_FILE=${MMS_HOME}/conf/conf-mms.properties
ENV MMS_CONF_FILE=${MMS_HOME}/conf/mms.conf
ENV MMS_LOG_DIR=${MMS_HOME}/logs
ENV MMS_TMP_DIR=${MMS_HOME}/tmp

EXPOSE 8080

COPY --from=rootfs_builder /mnt/rootfs/ /

COPY --from=base /data/licenses /licenses/
COPY --from=base /data/scripts /opt/scripts

COPY --from=om_downloader "${MMS_HOME}" "${MMS_HOME}"

USER 2000

# operator to change the entrypoint to: /mongodb-ops-manager/bin/mongodb-mms start_mms (or a wrapper around this)
ENTRYPOINT [ "sleep infinity" ]
