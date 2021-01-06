# Build compilable stuff

FROM golang:1.13-alpine as builder

COPY ./probe /build/
COPY ./probe_go_mod /build/
WORKDIR /build
RUN CGO_ENABLED=0 GOOS=linux GOARCH=386 go build -a -i -o readinessprobe .


FROM scratch
ARG mongodb_tools_url_ubi
ARG mongodb_tools_url_ubuntu

COPY --from=builder /build/readinessprobe /data/

ADD ${mongodb_tools_url_ubi} /data/mongodb_tools_ubi.tgz
ADD ${mongodb_tools_url_ubuntu} /data/mongodb_tools_ubuntu.tgz

COPY ./docker/mongodb-enterprise-init-database/content/probe.sh /data/probe.sh

COPY ./docker/mongodb-enterprise-init-database/content/agent-launcher-lib.sh /data/scripts/
COPY ./docker/mongodb-enterprise-init-database/content/agent-launcher.sh /data/scripts/

COPY ./docker/mongodb-enterprise-init-database/content/LICENSE /data/mongodb-enterprise-appdb
