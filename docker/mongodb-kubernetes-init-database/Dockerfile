FROM public.ecr.aws/docker/library/golang:1.24 AS readiness_builder

COPY . /go/src/github.com/mongodb/mongodb-kubernetes
WORKDIR /go/src/github.com/mongodb/mongodb-kubernetes
RUN CGO_ENABLED=0 GOFLAGS=-buildvcs=false go build -o /readinessprobe ./mongodb-community-operator/cmd/readiness/main.go
RUN CGO_ENABLED=0 GOFLAGS=-buildvcs=false go build -o /version-upgrade-hook ./mongodb-community-operator/cmd/versionhook/main.go

FROM scratch AS base

ARG mongodb_tools_url_ubi

COPY --from=readiness_builder /readinessprobe /data/
COPY --from=readiness_builder /version-upgrade-hook /data/version-upgrade-hook

ADD ${mongodb_tools_url_ubi} /data/mongodb_tools_ubi.tgz

COPY ./docker/mongodb-kubernetes-init-database/content/probe.sh /data/probe.sh

COPY ./docker/mongodb-kubernetes-init-database/content/agent-launcher-lib.sh /data/scripts/
COPY ./docker/mongodb-kubernetes-init-database/content/agent-launcher.sh /data/scripts/

COPY ./docker/mongodb-kubernetes-init-database/content/LICENSE /data/licenses/

FROM registry.access.redhat.com/ubi8/ubi-minimal

ARG version
LABEL name="MongoDB Kubernetes Init Database" \
      version="mongodb-kubernetes-init-database-${version}" \
      summary="MongoDB Kubernetes Database Init Image" \
      description="Startup Scripts for MongoDB Enterprise Database" \
      release="1" \
      vendor="MongoDB" \
      maintainer="support@mongodb.com"

COPY --from=base /data/readinessprobe /probes/readinessprobe
COPY --from=base /data/probe.sh /probes/probe.sh
COPY --from=base /data/scripts/ /scripts/
COPY --from=base /data/licenses /licenses/

RUN microdnf -y update --nodocs \
    && microdnf -y install --nodocs tar gzip \
    && microdnf clean all

COPY --from=base /data/mongodb_tools_ubi.tgz  /tools/mongodb_tools.tgz

RUN tar xfz /tools/mongodb_tools.tgz --directory /tools \
    && rm /tools/mongodb_tools.tgz

USER 2000
ENTRYPOINT [ "/bin/cp", "-f", "-r", "/scripts/agent-launcher.sh", "/scripts/agent-launcher-lib.sh", "/probes/readinessprobe", "/probes/probe.sh", "/tools", "/opt/scripts/" ]
