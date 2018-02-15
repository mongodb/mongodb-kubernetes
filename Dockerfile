# This is the Docker file for Ops Manager Operator
#
# To build the actual operator:
#
# $ CGO_ENABLED=0 GOOS=linux go build -o om-operator
#
# And then build the image:
#
# $ docker build -t om-operator:0.1 .
#
#
FROM scratch
MAINTAINER Ops Manager Team <mms@10gen.com>

ADD om-operator /usr/local/bin/

ENTRYPOINT ["/ur/local/bin/om-operator"]
