#
## Database image
#
## Contents
#
# * mongodb_server [ubi ubuntu]
# * mongodb_agent [ubi ubuntu]
# * licenses/mongodb-enterprise-database


FROM scratch

ARG mongodb_server_ubi
ARG mongodb_server_ubuntu
ARG mongodb_agent_linux

# Bundled mongodb server (distro-dependant)
ADD ${mongodb_server_ubi} /data/mongodb_server_ubi.tgz
ADD ${mongodb_server_ubuntu} /data/mongodb_server_ubuntu.tgz

# Bundled mongodb agent (linux-generic)
ADD ${mongodb_agent_linux} /data/mongodb_agent_linux.tgz

COPY LICENSE /data/licenses/mongodb-enterprise-database
