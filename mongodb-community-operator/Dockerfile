# This image is based on the official Golang image.
# This setup is for running our e2e Golang tests.
#
ARG GOLANG_VERSION

FROM public.ecr.aws/docker/library/golang:${GOLANG_VERSION} as builder

ENV GO111MODULE=on
ENV GOPATH ""

WORKDIR /workspace

COPY go.mod go.sum ./
RUN go mod download && go mod verify

COPY testdata/tls/ testdata/tls/

RUN curl -LO "https://storage.googleapis.com/kubernetes-release/release/$(curl -s https://storage.googleapis.com/kubernetes-release/release/stable.txt)/bin/linux/amd64/kubectl" \
    && chmod +x ./kubectl \
    && mv ./kubectl /usr/local/bin/kubectl \
    && curl -fsSL -o get_helm.sh https://raw.githubusercontent.com/helm/helm/master/scripts/get-helm-3 \
    && chmod +x get_helm.sh && ./get_helm.sh

ADD . .
