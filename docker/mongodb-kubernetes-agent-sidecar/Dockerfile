FROM --platform=$BUILDPLATFORM public.ecr.aws/docker/library/golang:1.24 AS builder

WORKDIR /go/src/github.com/mongodb/mongodb-kubernetes/

COPY go.mod go.sum ./

RUN go mod download

COPY mongodb-community-operator /go/src/github.com/mongodb/mongodb-kubernetes/mongodb-community-operator

ARG TARGETARCH
ARG TARGETOS
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -a -o /data/scripts/agent-sidecar ./mongodb-community-operator/cmd/agentsidecar/main.go

FROM registry.access.redhat.com/ubi9/ubi-minimal

COPY --from=builder /data/scripts/agent-sidecar /agent-sidecar

ENTRYPOINT ["/agent-sidecar"]
