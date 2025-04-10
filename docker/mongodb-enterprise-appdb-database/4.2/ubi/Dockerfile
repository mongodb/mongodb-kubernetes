FROM registry.access.redhat.com/ubi8/ubi

ARG MONGO_VERSION

LABEL name="MongoDB Enterprise AppDB Database" \
      version=${MONGO_VERSION} \
      summary="MongoDB Enterprise AppDB Database" \
      description="MongoDB Enterprise AppDB Database" \
      vendor="MongoDB" \
      release="1" \
      maintainer="support@mongodb.com"

ADD licenses /licenses

# add our user and group first to make sure their IDs get assigned consistently, regardless of whatever dependencies get added
RUN groupadd -r mongodb && useradd -r -g mongodb mongodb


RUN set -eux; \
    yum install -y --disableplugin=subscription-manager \
		ca-certificates \
    jq \
	; \
	if ! command -v ps > /dev/null; then \
    yum install -y --disableplugin=subscription-managers procps; \
	fi;

# grab gosu for easy step-down from root (https://github.com/tianon/gosu/releases)
ENV GOSU_VERSION 1.14
# grab "js-yaml" for parsing mongod's YAML config files (https://github.com/nodeca/js-yaml/releases)
ENV JSYAML_VERSION 3.13.1

RUN set -ex; \
	\
    yum install -y --disableplugin=subscription-manager \
		wget \
	; \
	if ! command -v gpg > /dev/null; then \
    yum install -y --disableplugin=subscription-manager gnupg dirmngr; \
	elif gpg --version | grep -q '^gpg (GnuPG) 1\.'; then \
# "This package provides support for HKPS keyservers." (GnuPG 1.x only)
    yum install -y --disableplugin=subscription-manager gnupg-curl; \
	fi; \
    \
    dpkgArch="$(arch| sed 's/x86_64/amd64/')"; \
	wget -O /usr/local/bin/gosu "https://github.com/tianon/gosu/releases/download/$GOSU_VERSION/gosu-$dpkgArch"; \
	wget -O /usr/local/bin/gosu.asc "https://github.com/tianon/gosu/releases/download/$GOSU_VERSION/gosu-$dpkgArch.asc"; \
	export GNUPGHOME="$(mktemp -d)"; \
	gpg --batch --keyserver hkps://keys.openpgp.org --recv-keys B42F6819007F00F88E364FD4036A9C25BF357DD4; \
	gpg --batch --verify /usr/local/bin/gosu.asc /usr/local/bin/gosu; \
	command -v gpgconf && gpgconf --kill all || :; \
	rm -r "$GNUPGHOME" /usr/local/bin/gosu.asc; \
	\
	wget -O /js-yaml.js "https://github.com/nodeca/js-yaml/raw/${JSYAML_VERSION}/dist/js-yaml.js"; \
	chmod +x /usr/local/bin/gosu; \
	gosu --version; \
	gosu nobody true

 RUN mkdir /docker-entrypoint-initdb.d

# # Allow build-time overrides (eg. to build image with MongoDB Enterprise version)
# # Options for MONGO_PACKAGE: mongodb-org OR mongodb-enterprise
# # Options for MONGO_REPO: repo.mongodb.org OR repo.mongodb.com
# # Example: docker build --build-arg MONGO_PACKAGE=mongodb-enterprise --build-arg MONGO_REPO=repo.mongodb.com .
ARG MONGO_PACKAGE=mongodb-org
ARG MONGO_REPO=repo.mongodb.org
ENV MONGO_PACKAGE=${MONGO_PACKAGE} MONGO_REPO=${MONGO_REPO}

ENV MONGO_MAJOR 4.2

COPY 4.2/ubi/mongodb-org-4.2.repo /etc/yum.repos.d/mongodb-org-4.2.repo


RUN cat /etc/yum.repos.d/mongodb-org-4.2.repo

RUN yum install -y mongodb-enterprise-${MONGO_VERSION} mongodb-enterprise-server-${MONGO_VERSION} mongodb-enterprise-shell-${MONGO_VERSION} mongodb-enterprise-mongos-${MONGO_VERSION} mongodb-enterprise-tools-${MONGO_VERSION}

RUN echo "exclude=mongodb-org,mongodb-org-server,mongodb-org-shell,mongodb-org-mongos,mongodb-org-tools" >> /etc/yum.conf
RUN mkdir -p /data/db /data/configdb \
	&& chown -R mongodb:mongodb /data/db /data/configdb
VOLUME /data/db /data/configdb

COPY docker-entrypoint.sh /usr/local/bin/
ENTRYPOINT ["docker-entrypoint.sh"]

EXPOSE 27017
CMD ["mongod"]
