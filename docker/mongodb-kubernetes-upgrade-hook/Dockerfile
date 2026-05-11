FROM --platform=$BUILDPLATFORM public.ecr.aws/docker/library/golang:1.25 AS builder

WORKDIR /go/src/github.com/mongodb/mongodb-kubernetes/

COPY go.mod go.sum ./

RUN go mod download

COPY mongodb-community-operator /go/src/github.com/mongodb/mongodb-kubernetes/mongodb-community-operator
# The versionhook cmd transitively imports shared packages relocated to root
# pkg/ as part of the mongodb-community-operator decoupling refactor; the build
# context must include them so `go build` can resolve those imports.
COPY pkg /go/src/github.com/mongodb/mongodb-kubernetes/pkg

ARG TARGETARCH
ARG TARGETOS
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -a -o /data/scripts/version-upgrade-hook ./mongodb-community-operator/cmd/versionhook/main.go

FROM registry.access.redhat.com/ubi9/ubi-minimal

COPY --from=builder /data/scripts/version-upgrade-hook /version-upgrade-hook
