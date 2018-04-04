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
#
FROM ubuntu:14.04
MAINTAINER Ops Manager Team <mms@10gen.com>

ADD om-operator /usr/local/bin/

ENTRYPOINT exec /usr/local/bin/om-operator -env=$ENVIRONMENT
