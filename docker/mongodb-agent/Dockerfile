# the init database image gets supplied by pipeline.py and corresponds to the operator version we want to release
# the agent with. This enables us to release the agent for older operator.
ARG init_database_image
FROM ${init_database_image} AS init_database

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

FROM scratch AS agent_downloader

ARG mongodb_agent_url

ARG mongodb_agent_version_amd64
ADD "${mongodb_agent_url}/${mongodb_agent_version_amd64}" /data/amd64/mongodb_agent.tgz

ARG mongodb_agent_version_arm64
ADD "${mongodb_agent_url}/${mongodb_agent_version_arm64}" /data/arm64/mongodb_agent.tgz

ARG mongodb_agent_version_s390x
ADD "${mongodb_agent_url}/${mongodb_agent_version_s390x}" /data/s390x/mongodb_agent.tgz

ARG mongodb_agent_version_ppc64le
ADD "${mongodb_agent_url}/${mongodb_agent_version_ppc64le}" /data/ppc64le/mongodb_agent.tgz

FROM --platform=$BUILDPLATFORM public.ecr.aws/docker/library/golang:1.24 AS readiness_builder

WORKDIR /go/src/github.com/mongodb/mongodb-kubernetes/

COPY go.mod go.sum ./

RUN go mod download

COPY mongodb-community-operator /go/src/github.com/mongodb/mongodb-kubernetes/mongodb-community-operator

ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOFLAGS=-buildvcs=false GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -o /readinessprobe ./mongodb-community-operator/cmd/readiness/main.go
RUN CGO_ENABLED=0 GOFLAGS=-buildvcs=false GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -o /version-upgrade-hook ./mongodb-community-operator/cmd/versionhook/main.go

FROM registry.access.redhat.com/ubi9/ubi-minimal

ARG TARGETARCH
COPY --from=tools_downloader "/data/${TARGETARCH}/mongodb_tools.tgz" /tools/mongodb_tools.tgz
COPY --from=agent_downloader "/data/${TARGETARCH}/mongodb_agent.tgz" /agent/mongodb_agent.tgz

COPY --from=readiness_builder /readinessprobe /opt/scripts/readinessprobe
COPY --from=readiness_builder /version-upgrade-hook /opt/scripts/version-upgrade-hook

COPY --from=init_database /probes/probe.sh /opt/scripts/probe.sh
COPY --from=init_database /scripts/agent-launcher-lib.sh /opt/scripts/agent-launcher-lib.sh
COPY --from=init_database /scripts/agent-launcher.sh /opt/scripts/agent-launcher.sh
COPY --from=init_database /licenses/LICENSE /licenses/LICENSE

# Replace libcurl-minimal and curl-minimal with the full versions
# https://bugzilla.redhat.com/show_bug.cgi?id=1994521
RUN  microdnf install -y libssh libpsl libbrotli \
    && microdnf download curl libcurl \
    && rpm -Uvh --nodeps --replacefiles "*curl*$( uname -i ).rpm" \
    && microdnf remove -y libcurl-minimal curl-minimal

RUN microdnf install -y --disableplugin=subscription-manager --setopt=install_weak_deps=0 nss_wrapper
# Copy-pasted from https://www.mongodb.com/docs/manual/tutorial/install-mongodb-enterprise-on-red-hat-tarball/
RUN microdnf install -y --disableplugin=subscription-manager \
 cyrus-sasl cyrus-sasl-gssapi cyrus-sasl-plain krb5-libs openldap openssl xz-libs
# Dependencies for the Agent
RUN microdnf install -y --disableplugin=subscription-manager  --setopt=install_weak_deps=0 \
        net-snmp \
        net-snmp-agent-libs
RUN microdnf install -y --disableplugin=subscription-manager \
    hostname tar gzip procps jq \
    && microdnf upgrade -y  \
    && rm -rf /var/lib/apt/lists/*

RUN tar xfz /tools/mongodb_tools.tgz
RUN mv mongodb-database-tools-*/bin/* /tools
RUN chmod +x /tools/*
RUN rm /tools/mongodb_tools.tgz
RUN rm -rf /mongodb-database-tools-*

RUN tar xfz /agent/mongodb_agent.tgz
RUN mv mongodb-mms-automation-agent-*/mongodb-mms-automation-agent /agent/mongodb-agent
RUN chmod +x /agent/mongodb-agent
RUN rm /agent/mongodb_agent.tgz
RUN rm -rf mongodb-mms-automation-agent-*

RUN mkdir -p /var/lib/automation/config
RUN chmod -R +r /var/lib/automation/config

ARG version

LABEL name="MongoDB Agent" \
      version="${version}" \
      summary="MongoDB Agent" \
      description="MongoDB Agent" \
      vendor="MongoDB" \
      release="1" \
      maintainer="support@mongodb.com"

USER 2000

HEALTHCHECK --timeout=30s CMD ls /opt/scripts/readinessprobe || exit 1
