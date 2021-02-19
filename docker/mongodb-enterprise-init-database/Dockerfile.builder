# Build compilable stuff

ARG readiness_probe_version
FROM quay.io/mongodb/mongodb-kubernetes-readinessprobe:${readiness_probe_version} as builder

FROM scratch
ARG mongodb_tools_url_ubi
ARG mongodb_tools_url_ubuntu

COPY --from=builder /probes/readinessprobe /data/

ADD ${mongodb_tools_url_ubi} /data/mongodb_tools_ubi.tgz
ADD ${mongodb_tools_url_ubuntu} /data/mongodb_tools_ubuntu.tgz

COPY ./docker/mongodb-enterprise-init-database/content/probe.sh /data/probe.sh

COPY ./docker/mongodb-enterprise-init-database/content/agent-launcher-lib.sh /data/scripts/
COPY ./docker/mongodb-enterprise-init-database/content/agent-launcher.sh /data/scripts/

COPY ./docker/mongodb-enterprise-init-database/content/LICENSE /data/licenses/
