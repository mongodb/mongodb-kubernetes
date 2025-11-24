FROM --platform=${BUILDPLATFORM} public.ecr.aws/docker/library/golang:1.24 AS builder

ARG BUILDARCH
ADD "https://github.com/stedolan/jq/releases/download/jq-1.8.1/jq-linux-${BUILDARCH}" /usr/local/bin/jq
RUN chmod +x /usr/local/bin/jq

COPY go.sum go.mod /go/src/github.com/mongodb/mongodb-kubernetes/

WORKDIR /go/src/github.com/mongodb/mongodb-kubernetes
RUN go mod download

COPY . /go/src/github.com/mongodb/mongodb-kubernetes

ARG version
ARG log_automation_config_diff
ARG use_race
ARG TARGETOS
ARG TARGETARCH

RUN mkdir /build && \
    if [ $use_race = "true" ]; then \
        echo "Building with race detector" && \
        CGO_ENABLED=1 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -o /build/mongodb-kubernetes-operator \
        -buildvcs=false \
        -race \
        -ldflags=" -X github.com/mongodb/mongodb-kubernetes/pkg/util.OperatorVersion=${version} \
        -X github.com/mongodb/mongodb-kubernetes/pkg/util.LogAutomationConfigDiff=${log_automation_config_diff}"; \
    else \
        echo "Building without race detector" && \
        CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -o /build/mongodb-kubernetes-operator \
        -buildvcs=false \
        -ldflags="-s -w -X github.com/mongodb/mongodb-kubernetes/pkg/util.OperatorVersion=${version} \
        -X github.com/mongodb/mongodb-kubernetes/pkg/util.LogAutomationConfigDiff=${log_automation_config_diff}"; \
    fi

RUN mkdir -p /data
RUN cat release.json | jq -r '.supportedImages."mongodb-agent" | { "supportedImages": { "mongodb-agent": . } }' > /data/om_version_mapping.json
RUN chmod +r /data/om_version_mapping.json

FROM scratch AS base

COPY --from=builder /build/mongodb-kubernetes-operator /data/
COPY --from=builder /data/om_version_mapping.json /data/om_version_mapping.json

ADD docker/mongodb-kubernetes-operator/licenses /data/licenses/

FROM podman pull quay.io/mircea_cosbuc/ubi-micro-operator-base

COPY --from=base /data/mongodb-kubernetes-operator /usr/local/bin/mongodb-kubernetes-operator
COPY --from=base /data/om_version_mapping.json /usr/local/om_version_mapping.json
COPY --from=base /data/licenses /licenses/

ARG version

LABEL name="MongoDB Kubernetes Operator" \
      maintainer="support@mongodb.com" \
      vendor="MongoDB" \
      version="${version}" \
      release="1" \
      summary="MongoDB Kubernetes Operator Image" \
      description="MongoDB Kubernetes Operator Image"

USER 2000

ENTRYPOINT exec /usr/local/bin/mongodb-kubernetes-operator
