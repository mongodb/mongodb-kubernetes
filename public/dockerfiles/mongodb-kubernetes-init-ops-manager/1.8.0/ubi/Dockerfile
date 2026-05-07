FROM --platform=${BUILDPLATFORM} public.ecr.aws/docker/library/golang:1.25 AS base

COPY . /go/src/github.com/mongodb/mongodb-kubernetes
WORKDIR /go/src/github.com/mongodb/mongodb-kubernetes

ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -a -buildvcs=false -o /data/scripts/mmsconfiguration ./docker/mongodb-kubernetes-init-ops-manager/mmsconfiguration
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -a -buildvcs=false -o /data/scripts/backup-daemon-readiness-probe ./docker/mongodb-kubernetes-init-ops-manager/backupdaemon_readinessprobe/

COPY ./docker/mongodb-kubernetes-init-ops-manager/scripts/docker-entry-point.sh /data/scripts/
COPY ./docker/mongodb-kubernetes-init-ops-manager/scripts/backup-daemon-liveness-probe.sh /data/scripts/
COPY ./docker/mongodb-kubernetes-init-ops-manager/LICENSE /data/licenses/mongodb-enterprise-ops-manager

FROM registry.access.redhat.com/ubi9/ubi-minimal

ARG version

LABEL name="MongoDB Kubernetes Ops Manager Init" \
      maintainer="support@mongodb.com" \
      vendor="MongoDB" \
      version="mongodb-kubernetes-init-ops-manager-${version}" \
      release="1" \
      summary="MongoDB Kubernetes Ops Manager Init Image" \
      description="Startup Scripts for MongoDB Enterprise Ops Manager"

COPY --from=base /data/scripts /scripts
COPY --from=base /data/licenses /licenses

RUN microdnf -y update --nodocs \
    && microdnf clean all

USER 2000
ENTRYPOINT [ "/bin/cp", "-f", "/scripts/docker-entry-point.sh", "/scripts/backup-daemon-liveness-probe.sh",  "/scripts/mmsconfiguration", "/scripts/backup-daemon-readiness-probe", "/opt/scripts/" ]
