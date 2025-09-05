FROM --platform=${BUILDPLATFORM} registry.access.redhat.com/ubi8/ubi-minimal AS tools_downloader

ARG mongodb_tools_url
ARG mongodb_tools_version_s390x
ARG mongodb_tools_version_ppc64le
ARG mongodb_tools_version_amd64
ARG mongodb_tools_version_arm64

RUN microdnf -y update --nodocs \
  && microdnf -y install --nodocs tar gzip curl \
  && microdnf clean all

RUN case ${TARGETPLATFORM} in \
     "linux/amd64")   export MONGODB_TOOLS_VERSION=${mongodb_tools_version_amd64}    ;; \
     "linux/arm64")   export MONGODB_TOOLS_VERSION=${mongodb_tools_version_arm64}    ;; \
     "linux/s390x")   export MONGODB_TOOLS_VERSION=${mongodb_tools_version_s390x}    ;; \
     "linux/ppc64le") export MONGODB_TOOLS_VERSION=${mongodb_tools_version_ppc64le}  ;; \
  esac \
    && mkdir -p /tools \
    && if [ -n "$MONGODB_TOOLS_VERSION" ];   then \
         curl -o /tools/mongodb_tools.tgz "${mongodb_tools_url}/${MONGODB_TOOLS_VERSION}"; \
       fi

RUN if [ -f "/tools/mongodb_tools.tgz" ]; then \
      tar xfz /tools/mongodb_tools.tgz --directory /tools \
      && rm /tools/mongodb_tools.tgz; \
    fi

FROM --platform=$BUILDPLATFORM public.ecr.aws/docker/library/golang:1.24 AS readiness_builder

WORKDIR /go/src/github.com/mongodb/mongodb-kubernetes/

COPY go.mod go.sum ./

RUN go mod download

COPY mongodb-community-operator ./mongodb-community-operator

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

FROM registry.access.redhat.com/ubi8/ubi-minimal

# Copy the extracted tools from the downloader stage (tools are already extracted there)
COPY --from=tools_downloader /tools/ /tools/

COPY --from=base /data/readinessprobe /probes/readinessprobe
COPY --from=base /data/probe.sh /probes/probe.sh
COPY --from=base /data/scripts/ /scripts/
COPY --from=base /data/licenses /licenses/

RUN microdnf -y update --nodocs \
    && microdnf -y install --nodocs tar gzip \
    && microdnf clean all

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
