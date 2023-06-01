# Build compilable stuff

ARG readiness_probe_repo
ARG readiness_probe_version
ARG version_upgrade_post_start_hook_version

FROM ${readiness_probe_repo}:${readiness_probe_version} as readiness_builder
FROM quay.io/mongodb/mongodb-kubernetes-operator-version-upgrade-post-start-hook:${version_upgrade_post_start_hook_version} as version_upgrade_builder

FROM scratch
ARG mongodb_tools_url_ubi

COPY --from=readiness_builder /probes/readinessprobe /data/
COPY --from=version_upgrade_builder /version-upgrade-hook /data/version-upgrade-hook

ADD ${mongodb_tools_url_ubi} /data/mongodb_tools_ubi.tgz

COPY ./docker/mongodb-enterprise-init-database/content/probe.sh /data/probe.sh

COPY ./docker/mongodb-enterprise-init-database/content/agent-launcher-lib.sh /data/scripts/
COPY ./docker/mongodb-enterprise-init-database/content/agent-launcher.sh /data/scripts/

COPY ./docker/mongodb-enterprise-init-database/content/LICENSE /data/licenses/
