#
# Dockerfile for Init Ops Manager Image.
# to be called from github root (to be able to include probe code):
# …/10gen/ops-manager-kubernetes] docker build -f docker/mongodb-enterprise-init-appdb/Dockerfile .
#

FROM golang:1.13-alpine as builder

RUN mkdir /build
COPY ./probe /build/
COPY ./probe_go_mod /build/
WORKDIR /build
RUN CGO_ENABLED=0 GOOS=linux GOARCH=386 go build -a -i -o readinessprobe .

FROM busybox

ARG VERSION

LABEL name="MongoDB Enterprise Init AppDB" \
      maintainer="support@mongodb.com" \
      vendor="MongoDB" \
      version="mongodb-enterprise-init-appdb-${VERSION}" \
      release="1" \
      summary="MongoDB Enterprise AppDB Init Image" \
      description="Startup Scripts for MongoDB Enterprise Application Database for Ops Manager"


RUN mkdir /probes
COPY --from=builder /build/readinessprobe /probes/
COPY ./docker_content/probe.sh /probes/

RUN mkdir /scripts
COPY ./docker_content/agent-launcher-lib.sh /scripts/
COPY ./docker_content/agent-launcher.sh /scripts/

ENTRYPOINT [ "/bin/cp", "-f", "/scripts/agent-launcher.sh", "/scripts/agent-launcher-lib.sh", "/probes/readinessprobe", "/probes/probe.sh", "/opt/scripts/" ]
