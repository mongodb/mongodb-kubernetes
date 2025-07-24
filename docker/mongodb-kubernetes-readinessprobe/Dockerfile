FROM public.ecr.aws/docker/library/golang:1.24 AS builder

WORKDIR /go/src
ADD . .

ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} go build -a -o /data/scripts/readinessprobe ./mongodb-community-operator/cmd/readiness/main.go

FROM registry.access.redhat.com/ubi9/ubi-minimal

COPY --from=builder /data/scripts/readinessprobe /probes/readinessprobe
