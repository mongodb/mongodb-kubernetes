#
# ©, 2018, the Docker Community. https://github.com/docker-library/mongo
#

FROM debian:9.5-slim

RUN apt-get update \
	&& apt-get install -y --no-install-recommends \
		ca-certificates \
		dirmngr \
		gnupg2 \
		numactl \
	&& rm -rf /var/lib/apt/lists/*

# Mongo version is a mandatory parameter
ARG MONGO_VERSION

RUN test -n "$MONGO_VERSION" || \
 ( >&2 echo "Necessary argument MONGO_VERSION is not specified, provide it using \"--build-arg MONGO_VERSION=...\"" && exit 1 )

ENV GPG_KEY "9DA31620334BD75D9DCB49F368818C72E52529D4"
ENV MONGO_MAJOR 4.0
ENV DATA_DIR "/data"
ENV JOURNAL_DIR "/journal"
ENV LOGS_DIR "/var/log/mongodb"

RUN apt-key adv --keyserver hkp://keyserver.ubuntu.com:80 --recv $GPG_KEY \
    && echo "deb http://repo.mongodb.com/apt/debian stretch/mongodb-enterprise/$MONGO_MAJOR main" | tee /etc/apt/sources.list.d/mongodb-enterprise.list

RUN set -x \
	&& apt-get update \
	&& apt-get install -y \
		mongodb-enterprise=$MONGO_VERSION \
		mongodb-enterprise-mongos=$MONGO_VERSION \
		mongodb-enterprise-server=$MONGO_VERSION \
		mongodb-enterprise-shell=$MONGO_VERSION \
		mongodb-enterprise-tools=$MONGO_VERSION \
	&& rm -rf /var/lib/apt/lists/* \
	&& rm -rf /var/lib/mongodb

RUN \
    mkdir -p $DATA_DIR && \
    mkdir -p $JOURNAL_DIR && \
    mkdir -p $LOGS_DIR && \
    chmod g+w $DATA_DIR && \
    chmod g+w $JOURNAL_DIR && \
    chmod g+w $LOGS_DIR && \
    ln -sf /dev/stdout "$LOGS_DIR/mongodb.log"


COPY docker-entrypoint.sh /usr/local/bin/
ENTRYPOINT ["docker-entrypoint.sh"]

CMD ["mongod"]
