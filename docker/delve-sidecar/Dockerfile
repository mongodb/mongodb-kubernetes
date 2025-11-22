FROM public.ecr.aws/docker/library/golang:1.24

RUN go install github.com/go-delve/delve/cmd/dlv@latest
RUN apt-get update && apt-get install -y procps

COPY docker/delve-sidecar/attach-delve.sh /attach-delve.sh
RUN chmod +x /attach-delve.sh

ENTRYPOINT ["/attach-delve.sh"]
