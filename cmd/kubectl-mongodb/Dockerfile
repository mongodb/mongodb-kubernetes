FROM golang:1.26 AS builder
WORKDIR /go/src
ADD . .

RUN CGO_ENABLED=0 go build -a -buildvcs=false -o /go/bin/mongodb-multicluster

FROM scratch
COPY --from=builder /go/bin/mongodb-multicluster /go/bin/mongodb-multicluster

USER 65534:65534
ENTRYPOINT [ "/go/bin/mongodb-multicluster" ]
