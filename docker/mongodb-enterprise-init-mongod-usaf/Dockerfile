FROM registry.access.redhat.com/ubi8/ubi-minimal as builder

ARG mongod42version=4.2.8
ARG distro=rhel70
ARG toolsVersion=100.0.2

RUN curl -O https://downloads.mongodb.com/linux/mongodb-linux-x86_64-enterprise-${distro}-${mongod42version}.tgz \
    # Ops Manager 4.4 uses tools version 100.0.2, Ops Manager < 4.4 does not use mongodb tools and so it is not required
    && curl -O  https://fastdl.mongodb.org/tools/db/mongodb-database-tools-${distro}-x86_64-${toolsVersion}.tgz

RUN mkdir downloads && mv mongodb-linux-* downloads \
    && mv mongodb-database-tools-* /downloads

FROM busybox

RUN mkdir /binaries
COPY --from=builder /downloads/mongodb-linux-* /binaries/
COPY --from=builder /downloads/mongodb-database-tools-* /binaries/
