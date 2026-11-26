FROM --platform=$BUILDPLATFORM public.ecr.aws/docker/library/golang:1.26 AS builder

WORKDIR /go/src/github.com/mongodb/mongodb-kubernetes/

COPY go.mod go.sum ./

RUN go mod download

COPY cmd/mongodb-proxy ./cmd/mongodb-proxy
COPY pkg/mongodbproxy ./pkg/mongodbproxy

ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -a -o /data/mongodb-proxy ./cmd/mongodb-proxy

# The image is only ever mounted read-only as an OCI image volume at /mnt/proxy in the `mongodb`
# container, so it must contain nothing but the statically linked binary.
FROM scratch

COPY --from=builder /data/mongodb-proxy /proxy

USER 65534

ENTRYPOINT ["/proxy"]
