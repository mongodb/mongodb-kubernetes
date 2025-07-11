#
# Dockerfile for Operator.
# to be called from git root
# docker build . -f docker/mongodb-kubernetes-operator/Dockerfile.builder
#

FROM public.ecr.aws/docker/library/golang:1.24 as builder

ARG release_version
ARG log_automation_config_diff
ARG use_race
ARG TARGETOS
ARG TARGETARCH

COPY go.sum go.mod /go/src/github.com/mongodb/mongodb-kubernetes/

WORKDIR /go/src/github.com/mongodb/mongodb-kubernetes
RUN go mod download

COPY . /go/src/github.com/mongodb/mongodb-kubernetes

RUN go version
RUN git version
RUN mkdir /build && \
    if [ $use_race = "true" ]; then \
        echo "Building with race detector" && \
        CGO_ENABLED=1 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -o /build/mongodb-kubernetes-operator \
        -buildvcs=false \
        -race \
        -ldflags=" -X github.com/mongodb/mongodb-kubernetes/pkg/util.OperatorVersion=${release_version} \
        -X github.com/mongodb/mongodb-kubernetes/pkg/util.LogAutomationConfigDiff=${log_automation_config_diff}"; \
    else \
        echo "Building without race detector" && \
        CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -o /build/mongodb-kubernetes-operator \
        -buildvcs=false \
        -ldflags="-s -w -X github.com/mongodb/mongodb-kubernetes/pkg/util.OperatorVersion=${release_version} \
        -X github.com/mongodb/mongodb-kubernetes/pkg/util.LogAutomationConfigDiff=${log_automation_config_diff}"; \
    fi


ADD https://github.com/stedolan/jq/releases/download/jq-1.6/jq-linux64 /usr/local/bin/jq
RUN chmod +x /usr/local/bin/jq

RUN mkdir -p /data
RUN cat release.json | jq -r '.supportedImages."mongodb-agent" | { "supportedImages": { "mongodb-agent": . } }' > /data/om_version_mapping.json
RUN chmod +r /data/om_version_mapping.json

FROM scratch

COPY --from=builder /build/mongodb-kubernetes-operator /data/
COPY --from=builder /data/om_version_mapping.json /data/om_version_mapping.json

ADD docker/mongodb-kubernetes-operator/licenses /data/licenses/
