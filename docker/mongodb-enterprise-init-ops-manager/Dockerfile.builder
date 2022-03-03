#
# Dockerfile for Init Ops Manager Context.
#

FROM golang:1.17.7-alpine as builder
WORKDIR /go/src
ADD . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=386 go build -a -i -o /data/scripts/mmsconfiguration ./mmsconfiguration
RUN CGO_ENABLED=0 GOOS=linux GOARCH=386 go build -a -i -o /data/scripts/backup-daemon-readiness-probe ./backupdaemon_readinessprobe/

COPY scripts/docker-entry-point.sh /data/scripts/
COPY scripts/backup-daemon-liveness-probe.sh /data/scripts/

COPY LICENSE /data/licenses/mongodb-enterprise-ops-manager
