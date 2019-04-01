FROM ubuntu:16.04

# Add certificate in order to make the operator work with Cloud Manager
RUN apt-get -qq update && \
  apt-get -y -qq install ca-certificates && \
  apt-get upgrade -y -qq && \
  apt-get dist-upgrade -y -qq && \
  rm -rf /var/lib/apt/lists/*

ADD content/mongodb-enterprise-operator /usr/local/bin/

ENTRYPOINT exec /usr/local/bin/mongodb-enterprise-operator
