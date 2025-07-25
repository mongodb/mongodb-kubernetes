#
# Dockerfile for Init Ops Manager Context.
#

FROM public.ecr.aws/docker/library/golang:1.24 AS builder

COPY . /build
WORKDIR /build

RUN mkdir -p /data/scripts /data/licenses

RUN CGO_ENABLED=0 go build -a -buildvcs=false -o /data/scripts/mmsconfiguration ./docker/mongodb-kubernetes-init-ops-manager/mmsconfiguration
RUN CGO_ENABLED=0 go build -a -buildvcs=false -o /data/scripts/backup-daemon-readiness-probe ./docker/mongodb-kubernetes-init-ops-manager/backupdaemon_readinessprobe

COPY docker/mongodb-kubernetes-init-ops-manager/scripts/*.sh /data/scripts/

COPY docker/mongodb-kubernetes-init-ops-manager/LICENSE /data/licenses/mongodb-enterprise-ops-manager
