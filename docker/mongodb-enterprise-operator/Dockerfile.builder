#
# Dockerfile for Operator.
# to be called from git root
# docker build . -f docker/mongodb-enterprise-operator/Dockerfile.builder
#

FROM golang:1.13 as builder

ARG release_version
ARG log_automation_config_diff
ARG mdb_version


COPY go.sum go.mod /go/src/github.com/10gen/ops-manager-kubernetes/
WORKDIR /go/src/github.com/10gen/ops-manager-kubernetes
RUN go mod download

COPY . /go/src/github.com/10gen/ops-manager-kubernetes

RUN go mod vendor
RUN ./scripts/build/codegen.sh

RUN mkdir /build && go build -i -o /build/mongodb-enterprise-operator \
        -ldflags="-s -w -X github.com/10gen/ops-manager-kubernetes/pkg/util.OperatorVersion=${release_version} \
        -X github.com/10gen/ops-manager-kubernetes/pkg/util.LogAutomationConfigDiff=${log_automation_config_diff} \
        -X github.com/10gen/ops-manager-kubernetes/pkg/util.BundledAppDbMongoDBVersion=${mdb_version}"

RUN go get -u github.com/go-delve/delve/cmd/dlv

FROM scratch
ARG version_manifest_url

COPY --from=builder /go/bin/dlv /data/dlv
COPY --from=builder /build/mongodb-enterprise-operator /data/

ADD ${version_manifest_url} /data/version_manifest.json

ADD docker/mongodb-enterprise-operator/licenses /data/licenses/
