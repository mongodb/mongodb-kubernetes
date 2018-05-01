# This is the Docker file for Ops Manager Operator
#
# To build the actual operator:
#
# $ CGO_ENABLED=0 GOOS=linux go build -i -o om-operator
#
# And then build the image:
#
# $ docker build -t om-operator:0.1 .
#
# To run:
# $ docker run -e OM_OPERATOR_ENV=local om-operator:0.1
#
FROM debian:jessie-slim
MAINTAINER Ops Manager Team <mms@10gen.com>

# Add certificate in order to make the operator work with Cloud Manager
RUN apt-get update && apt-get install -y ca-certificates

COPY config /etc/om-operator/
ADD om-operator /usr/local/bin/

ENTRYPOINT exec /usr/local/bin/om-operator -env=$OM_OPERATOR_ENV
