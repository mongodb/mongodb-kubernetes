ARG imagebase
FROM ${imagebase} as base

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

COPY --from=base /data/mongodb_tools_ubi.tgz    /tools/mongodb_tools.tgz


RUN tar xfz /tools/mongodb_tools.tgz --directory /tools \
    && rm /tools/mongodb_tools.tgz

USER 2000
ENTRYPOINT [ "/bin/cp", "-f", "-r", "/scripts/agent-launcher.sh", "/scripts/agent-launcher-lib.sh", "/probes/readinessprobe", "/probes/probe.sh", "/tools", "/opt/scripts/" ]


