#
# Dockerfile for Init Ops Manager Context.
#

FROM golang:1.13-alpine as builder
ADD . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=386 go build -a -i -o /data/scripts/mmsconfiguration ./mmsconfiguration

COPY scripts/docker-entry-point.sh /data/scripts/
COPY LICENSE /data/licenses/mongodb-enterprise-ops-manager
