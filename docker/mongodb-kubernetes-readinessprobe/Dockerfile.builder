FROM public.ecr.aws/docker/library/golang:1.24 as builder
WORKDIR /go/src
ADD . .

ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -a -o /data/scripts/readinessprobe ./mongodb-community-operator/cmd/readiness/main.go

FROM scratch as final

COPY --from=builder /data/scripts/readinessprobe /probes/
