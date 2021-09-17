#
# Dockerfile for Operator.
# to be called from git root
# docker build . -f docker/mongodb-enterprise-operator/Dockerfile.builder
#

FROM golang:1.16 as builder

ARG release_version
ARG log_automation_config_diff


COPY go.sum go.mod /go/src/github.com/10gen/ops-manager-kubernetes/
WORKDIR /go/src/github.com/10gen/ops-manager-kubernetes
RUN go mod download


COPY . /go/src/github.com/10gen/ops-manager-kubernetes

RUN mkdir /build && go build -i -o /build/mongodb-enterprise-operator \
        -ldflags="-s -w -X github.com/10gen/ops-manager-kubernetes/pkg/util.OperatorVersion=${release_version} \
        -X github.com/10gen/ops-manager-kubernetes/pkg/util.LogAutomationConfigDiff=${log_automation_config_diff}"

ADD https://us-east-1.aws.webhooks.mongodb-realm.com/api/client/v2.0/app/kubernetes-version-mappings-aarzq/service/ops_manager_version_to_minimum_agent_version/incoming_webhook/list /data/om_version_mapping.json
RUN chmod +r /data/om_version_mapping.json

RUN go get github.com/go-delve/delve/cmd/dlv

FROM scratch

COPY --from=builder /go/bin/dlv /data/dlv
COPY --from=builder /build/mongodb-enterprise-operator /data/
COPY --from=builder /data/om_version_mapping.json /data/om_version_mapping.json

ADD docker/mongodb-enterprise-operator/licenses /data/licenses/
