ARG imagebase
FROM ${imagebase} as base

FROM registry.access.redhat.com/ubi9/ubi-minimal

LABEL name="MongoDB Kubernetes Ops Manager Init" \
      maintainer="support@mongodb.com" \
      vendor="MongoDB" \
      version="mongodb-kubernetes-init-ops-manager-1.0.0" \
      release="1" \
      summary="MongoDB Kubernetes Ops Manager Init Image" \
      description="Startup Scripts for MongoDB Enterprise Ops Manager"


COPY --from=base /data/scripts /scripts
COPY --from=base /data/licenses /licenses


RUN microdnf -y update --nodocs \
    && microdnf clean all


USER 2000
ENTRYPOINT [ "/bin/cp", "-f", "/scripts/docker-entry-point.sh", "/scripts/backup-daemon-liveness-probe.sh",  "/scripts/mmsconfiguration", "/scripts/backup-daemon-readiness-probe", "/opt/scripts/" ]


