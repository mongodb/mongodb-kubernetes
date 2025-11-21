FROM scratch AS base

COPY ./docker/mongodb-kubernetes-database/LICENSE /data/licenses/mongodb-kubernetes-database

FROM quay.io/mircea_cosbuc/ubi-micro-database-base

ENV MMS_HOME=/mongodb-automation
ENV MMS_LOG_DIR=/var/log/mongodb-mms-automation

# Set the required perms
RUN mkdir -p "${MMS_LOG_DIR}" \
        && chmod 0775 "${MMS_LOG_DIR}" \
        && mkdir -p /var/lib/mongodb-mms-automation \
        && chmod 0775 /var/lib/mongodb-mms-automation \
        && mkdir -p /data \
        && chmod 0775 /data \
        && mkdir -p /journal \
        && chmod 0775 /journal \
        && mkdir -p "${MMS_HOME}" \
        && chmod -R 0775 "${MMS_HOME}"

ARG version

LABEL name="MongoDB Kubernetes Database" \
      version="${version}" \
      summary="MongoDB Kubernetes Database Image" \
      description="MongoDB Kubernetes Database Image" \
      vendor="MongoDB" \
      release="1" \
      maintainer="support@mongodb.com"

# USER needs to be set for this image to pass RedHat verification. Some customers have these requirements as well
# It does not matter what number it is, as long as it is set to something.
# However, OpenShift will run the container as a random user,
# and the number in this configuration is not relevant.
USER 2000

# The docker image doesn't have any scripts so by default does nothing
# The script will be copied in runtime from init containers and the operator is expected
# to override the COMMAND
ENTRYPOINT ["sleep infinity"]

COPY --from=base /data/licenses/mongodb-kubernetes-database /licenses/mongodb-kubernetes-database
