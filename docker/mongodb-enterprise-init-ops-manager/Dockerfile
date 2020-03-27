#
# Dockerfile for Init Ops Manager Image.
#

FROM golang:alpine as builder
RUN mkdir /build
ADD . /build/
WORKDIR /build
RUN GOOS=linux go build -i -o mmsconfiguration ./mmsconfiguration

FROM busybox

ARG VERSION

LABEL name="MongoDB Enterprise Ops Manager Init" \
      maintainer="support@mongodb.com" \
      vendor="MongoDB" \
      version="mongodb-enterprise-init-ops-manager-${VERSION}" \
      release="1" \
      summary="MongoDB Enterprise Ops Manager Init Image" \
      description="Startup Scripts for MongoDB Enterprise Ops Manager"


RUN mkdir /scripts
COPY --from=builder /build/mmsconfiguration /scripts/
COPY scripts/docker-entry-point.sh /scripts/

ENTRYPOINT [ "/bin/cp", "-f", "/scripts/docker-entry-point.sh", "/scripts/mmsconfiguration", "/opt/scripts/" ]
