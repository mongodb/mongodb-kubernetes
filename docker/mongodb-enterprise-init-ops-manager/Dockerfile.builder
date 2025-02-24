#
# Dockerfile for Init Ops Manager Context.
#

FROM public.ecr.aws/docker/library/golang:1.24 as builder
WORKDIR /go/src
ADD . .
RUN CGO_ENABLED=0 go build -a -buildvcs=false -o /data/scripts/mmsconfiguration ./mmsconfiguration
RUN CGO_ENABLED=0 go build -a -buildvcs=false -o /data/scripts/backup-daemon-readiness-probe ./backupdaemon_readinessprobe/

COPY scripts/docker-entry-point.sh /data/scripts/
COPY scripts/backup-daemon-liveness-probe.sh /data/scripts/

COPY LICENSE /data/licenses/mongodb-enterprise-ops-manager
