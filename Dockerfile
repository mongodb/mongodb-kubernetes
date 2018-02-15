FROM scratch
MAINTAINER Ops Manager Team <mms@10gen.com>

ADD om-operator /usr/local/bin/

ENTRYPOINT ["/ur/local/bin/om-operator"]
