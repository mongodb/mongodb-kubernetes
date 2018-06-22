# This is the Docker file for Ops Manager Operator
#
# To build the actual operator:
#
# $ CGO_ENABLED=0 GOOS=linux go build -i -o mongodb-enterprise-operator
#
# And then build the image:
#
# $ docker build -t mongodb-enterprise-operator:0.1 .
#
# To run:
# $ docker run -e OPERATOR_ENV=local -e MONGODB_ENTERPRISE_DATABASE_IMAGE=mongodb-enterprise-database -e IMAGE_PULL_POLICY=Never mongodb-enterprise-operator:0.1

FROM debian:jessie-slim
MAINTAINER Ops Manager Team <noreply@mongodb.com>

# Add certificate in order to make the operator work with Cloud Manager
RUN apt-get -qq update && \
  apt-get -y -qq install ca-certificates && \
  apt-get upgrade -y -qq && \
  apt-get dist-upgrade -y -qq && \
  rm -rf /var/lib/apt/lists/*

ADD content/mongodb-enterprise-operator /usr/local/bin/

ENTRYPOINT exec /usr/local/bin/mongodb-enterprise-operator
