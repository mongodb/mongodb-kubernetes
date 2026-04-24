FROM public.ecr.aws/docker/library/golang:1.27 AS builder

ENV GO111MODULE=on
ENV GOPATH=""

WORKDIR /tests

COPY docker/mongodb-community-tests/testdata/tls/ testdata/tls/

RUN curl -LO "https://storage.googleapis.com/kubernetes-release/release/$(curl -s https://storage.googleapis.com/kubernetes-release/release/stable.txt)/bin/linux/amd64/kubectl" \
    && chmod +x ./kubectl \
    && mv ./kubectl /usr/local/bin/kubectl \
    && curl -fsSL -o get_helm.sh https://raw.githubusercontent.com/helm/helm/master/scripts/get-helm-3 \
    && chmod +x get_helm.sh && ./get_helm.sh


ADD . .

RUN go mod download && go mod verify
