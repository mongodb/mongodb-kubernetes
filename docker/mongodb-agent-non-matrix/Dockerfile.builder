FROM scratch

ARG agent_version
ARG agent_distro
ARG tools_distro
ARG tools_version

ADD https://mciuploads.s3.amazonaws.com/mms-automation/mongodb-mms-build-agent/builds/automation-agent/prod/mongodb-mms-automation-agent-${agent_version}.${agent_distro}.tar.gz /data/mongodb-agent.tar.gz
ADD https://downloads.mongodb.org/tools/db/mongodb-database-tools-${tools_distro}-${tools_version}.tgz /data/mongodb-tools.tgz

COPY ./docker/mongodb-kubernetes-init-database/content/LICENSE /data/LICENSE
COPY ./docker/mongodb-agent/agent-launcher-shim.sh /opt/scripts/agent-launcher-shim.sh
COPY ./docker/mongodb-agent/setup-agent-files.sh /opt/scripts/setup-agent-files.sh
COPY ./docker/mongodb-agent/dummy-probe.sh /opt/scripts/dummy-probe.sh
COPY ./docker/mongodb-agent/dummy-readinessprobe.sh /opt/scripts/dummy-readinessprobe.sh
