FROM scratch AS tools_downloader

ARG mongodb_tools_url

ARG mongodb_tools_version_amd64
ADD "${mongodb_tools_url}/${mongodb_tools_version_amd64}" /data/amd64/mongodb_tools.tgz

ARG mongodb_tools_version_arm64
ADD "${mongodb_tools_url}/${mongodb_tools_version_arm64}" /data/arm64/mongodb_tools.tgz

ARG mongodb_tools_version_s390x
ADD "${mongodb_tools_url}/${mongodb_tools_version_s390x}" /data/s390x/mongodb_tools.tgz

ARG mongodb_tools_version_ppc64le
ADD "${mongodb_tools_url}/${mongodb_tools_version_ppc64le}" /data/ppc64le/mongodb_tools.tgz

FROM --platform=$BUILDPLATFORM public.ecr.aws/docker/library/golang:1.24 AS readiness_builder

WORKDIR /go/src/github.com/mongodb/mongodb-kubernetes/

COPY go.mod go.sum ./

RUN go mod download

COPY mongodb-community-operator /go/src/github.com/mongodb/mongodb-kubernetes/mongodb-community-operator

ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOFLAGS=-buildvcs=false GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -o /readinessprobe ./mongodb-community-operator/cmd/readiness/main.go
RUN CGO_ENABLED=0 GOFLAGS=-buildvcs=false GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -o /version-upgrade-hook ./mongodb-community-operator/cmd/versionhook/main.go

FROM scratch AS base

COPY --from=readiness_builder /readinessprobe /data/readinessprobe
COPY --from=readiness_builder /version-upgrade-hook /data/version-upgrade-hook

COPY ./docker/mongodb-kubernetes-init-database/content/probe.sh /data/probe.sh

COPY ./docker/mongodb-kubernetes-init-database/content/agent-launcher-lib.sh /data/scripts/
COPY ./docker/mongodb-kubernetes-init-database/content/agent-launcher.sh /data/scripts/

COPY ./docker/mongodb-kubernetes-init-database/content/LICENSE /data/licenses/

#TODO ubi9?
FROM registry.access.redhat.com/ubi8/ubi-minimal

ARG TARGETARCH
COPY --from=tools_downloader /data/${TARGETARCH}/mongodb_tools.tgz /tools/mongodb_tools.tgz

RUN microdnf -y update --nodocs \
  && microdnf -y install --nodocs tar gzip \
  && microdnf clean all

RUN tar xfz /tools/mongodb_tools.tgz --directory /tools \
    && rm /tools/mongodb_tools.tgz

COPY --from=base /data/readinessprobe /probes/readinessprobe
COPY --from=base /data/probe.sh /probes/probe.sh
COPY --from=base /data/scripts/ /scripts/
COPY --from=base /data/licenses /licenses/

ARG version
LABEL name="MongoDB Kubernetes Init Database" \
      version="mongodb-kubernetes-init-database-${version}" \
      summary="MongoDB Kubernetes Database Init Image" \
      description="Startup Scripts for MongoDB Enterprise Database" \
      release="1" \
      vendor="MongoDB" \
      maintainer="support@mongodb.com"

USER 2000

ENTRYPOINT [ "/bin/cp", "-f", "-r", "/scripts/agent-launcher.sh", "/scripts/agent-launcher-lib.sh", "/probes/readinessprobe", "/probes/probe.sh", "/tools", "/opt/scripts/" ]
