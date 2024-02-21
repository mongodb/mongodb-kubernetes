# Build compilable stuff

FROM golang:1.21 as readiness_builder
COPY . /go/src/github.com/10gen/ops-manager-kubernetes
WORKDIR /go/src/github.com/10gen/ops-manager-kubernetes
RUN CGO_ENABLED=0 go build -o /readinessprobe github.com/mongodb/mongodb-kubernetes-operator/cmd/readiness
RUN CGO_ENABLED=0 go build -o /version-upgrade-hook github.com/mongodb/mongodb-kubernetes-operator/cmd/versionhook

FROM scratch
ARG mongodb_tools_url_ubi
ARG mongodb_agent_url_ubi

COPY --from=readiness_builder /readinessprobe /data/
COPY --from=readiness_builder /version-upgrade-hook /data/

ADD ${mongodb_tools_url_ubi} /data/mongodb_tools_ubi.tgz
ADD ${mongodb_agent_url_ubi} /data/mongodb_agent_ubi.tgz

# After v2.0, when non-Static Agent images will be removed, please ensure to copy those files
# into ./docker/mongodb-agent directory. Leaving it this way will make the maintenance easier.
COPY ./docker/mongodb-enterprise-init-database/content/probe.sh /data/probe.sh
COPY ./docker/mongodb-enterprise-init-database/content/agent-launcher-lib.sh /data/
COPY ./docker/mongodb-enterprise-init-database/content/agent-launcher.sh /data/
COPY ./docker/mongodb-enterprise-init-database/content/LICENSE /data/